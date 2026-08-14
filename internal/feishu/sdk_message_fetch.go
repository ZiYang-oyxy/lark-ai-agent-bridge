package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"lark-agent-bridge/internal/media"
)

const (
	maxMergeForwardMessages     = 200
	maxMergeForwardTextRunes    = 64 << 10
	maxReferencedMessageDepth   = 4
	maxReferencedMessageFetches = 20
)

// FetchedMessage carries the minimal fields bridge needs to inline a quoted
// (replied-to) message into an agent prompt.
type FetchedMessage struct {
	MessageID   string
	ChatID      string
	MessageType string
	Text        string
	SenderID    string
	SenderName  string
	// SenderType 是被引用消息发送者的主体类型，飞书 im.v1 message.Get 直接返回
	// (`user` / `app` / `anonymous` / `unknown`)。上层保留该事实供 audit 和路由使用。
	SenderType string
	// Attachments 是被引用消息里可下载的图片/文件引用。上层(topic seed quote 模式)
	// 用它把引用里的图片拉回来喂给 agent。文本消息返回 nil。
	Attachments []media.Ref
}

// GetMessageAPI is the subset of the Feishu im.v1 message API used to fetch a
// single message by id. Extracted as an interface so it can be faked in tests.
type GetMessageAPI interface {
	Get(ctx context.Context, req *larkim.GetMessageReq, options ...larkcore.RequestOptionFunc) (*larkim.GetMessageResp, error)
}

// FetchMessage retrieves a message by id and extracts its plain-text body.
// Feishu returns a merge_forward container and all of its snapshot children in
// the same items array; those children are reconstructed into readable context.
func (s *SDKSender) FetchMessage(ctx context.Context, messageID string) (FetchedMessage, error) {
	if messageID == "" {
		return FetchedMessage{}, fmt.Errorf("fetch feishu message: missing message id")
	}
	if s == nil || s.getAPI == nil {
		return FetchedMessage{}, fmt.Errorf("fetch feishu message: message API unavailable")
	}
	state := &messageFetchState{
		active: make(map[string]bool),
		cache:  make(map[string]messageFetchResult),
	}
	return s.fetchMessage(ctx, messageID, state, 0)
}

type messageFetchResult struct {
	message FetchedMessage
	err     error
}

type messageFetchState struct {
	active  map[string]bool
	cache   map[string]messageFetchResult
	fetches int
}

func (s *SDKSender) fetchMessage(ctx context.Context, messageID string, state *messageFetchState, depth int) (FetchedMessage, error) {
	if cached, ok := state.cache[messageID]; ok {
		return cached.message, cached.err
	}
	if depth > maxReferencedMessageDepth {
		return FetchedMessage{}, fmt.Errorf("fetch feishu message: quoted message depth exceeds %d", maxReferencedMessageDepth)
	}
	if state.active[messageID] {
		return FetchedMessage{}, fmt.Errorf("fetch feishu message: quoted message cycle at id=%s", messageID)
	}
	if state.fetches >= maxReferencedMessageFetches {
		return FetchedMessage{}, fmt.Errorf("fetch feishu message: quoted message fetch limit exceeds %d", maxReferencedMessageFetches)
	}
	state.fetches++
	state.active[messageID] = true
	defer delete(state.active, messageID)

	req := larkim.NewGetMessageReqBuilder().
		MessageId(messageID).
		CardMsgContentType("user_card_content").
		Build()
	resp, err := s.getAPI.Get(ctx, req)
	if err != nil {
		return cacheMessageFetch(state, messageID, FetchedMessage{}, fmt.Errorf("fetch feishu message: %w", err))
	}
	if resp == nil {
		return cacheMessageFetch(state, messageID, FetchedMessage{}, fmt.Errorf("fetch feishu message failed: empty response"))
	}
	if !resp.Success() {
		return cacheMessageFetch(state, messageID, FetchedMessage{}, fmt.Errorf("fetch feishu message failed: code=%d msg=%s", resp.Code, resp.Msg))
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 || resp.Data.Items[0] == nil {
		return cacheMessageFetch(state, messageID, FetchedMessage{}, fmt.Errorf("fetch feishu message failed: no items for id=%s", messageID))
	}
	item := rootFetchedMessage(resp.Data.Items, messageID)
	if item == nil {
		return cacheMessageFetch(state, messageID, FetchedMessage{}, fmt.Errorf("fetch feishu message failed: no root item for id=%s", messageID))
	}
	out := FetchedMessage{MessageID: messageID}
	if item.ChatId != nil {
		out.ChatID = *item.ChatId
	}
	if item.MsgType != nil {
		out.MessageType = *item.MsgType
	}
	if item.Body != nil && item.Body.Content != nil {
		if out.MessageType == "interactive" {
			out.Text = parseInteractiveMessageText(*item.Body.Content)
		} else {
			out.Text = parseMessageText(*item.Body.Content)
		}
		out.Attachments = parseMessageAttachments(messageID, out.MessageType, *item.Body.Content)
	}
	if item.Sender != nil {
		if item.Sender.Id != nil {
			out.SenderID = *item.Sender.Id
		}
		if item.Sender.SenderName != nil {
			out.SenderName = *item.Sender.SenderName
		}
		if item.Sender.SenderType != nil {
			out.SenderType = *item.Sender.SenderType
		}
	}
	if out.MessageType == "merge_forward" || hasMergeForwardChildren(resp.Data.Items, messageID) {
		out.MessageType = "merge_forward"
		out.Text, out.Attachments = s.renderMergeForward(ctx, messageID, resp.Data.Items, state, depth)
	}
	return cacheMessageFetch(state, messageID, out, nil)
}

func cacheMessageFetch(state *messageFetchState, messageID string, message FetchedMessage, err error) (FetchedMessage, error) {
	state.cache[messageID] = messageFetchResult{message: message, err: err}
	return message, err
}

func rootFetchedMessage(items []*larkim.Message, messageID string) *larkim.Message {
	for _, item := range items {
		if item != nil && stringPtr(item.MessageId) == messageID && stringPtr(item.UpperMessageId) == "" {
			return item
		}
	}
	for _, item := range items {
		if item != nil {
			return item
		}
	}
	return nil
}

func hasMergeForwardChildren(items []*larkim.Message, messageID string) bool {
	for _, item := range items {
		if item == nil {
			continue
		}
		if upper := stringPtr(item.UpperMessageId); upper != "" {
			return true
		}
		if id := stringPtr(item.MessageId); id != "" && id != messageID {
			return true
		}
	}
	return false
}

func (s *SDKSender) renderMergeForward(ctx context.Context, rootID string, items []*larkim.Message, state *messageFetchState, fetchDepth int) (string, []media.Ref) {
	children := make(map[string][]*larkim.Message)
	knownMessages := make(map[string]bool)
	total := 0
	for _, item := range items {
		if item == nil {
			continue
		}
		id := stringPtr(item.MessageId)
		if id != "" {
			knownMessages[id] = true
		}
		upper := stringPtr(item.UpperMessageId)
		if id == rootID && upper == "" {
			continue
		}
		if upper == "" {
			upper = rootID
		}
		children[upper] = append(children[upper], item)
		total++
	}
	for parent := range children {
		sort.SliceStable(children[parent], func(i, j int) bool {
			return messageTimestamp(children[parent][i]) < messageTimestamp(children[parent][j])
		})
	}

	var body strings.Builder
	attachments := make([]media.Ref, 0)
	rendered := 0
	path := make(map[string]bool)
	renderedQuotes := make(map[string]bool)
	var writeChildren func(string, int)
	writeChildren = func(parent string, treeDepth int) {
		for _, item := range children[parent] {
			if rendered >= maxMergeForwardMessages {
				return
			}
			rendered++
			indent := strings.Repeat("  ", treeDepth)
			fmt.Fprintf(&body, "%s%s\n", indent, mergeForwardMessageHeader(item))
			parentID := stringPtr(item.ParentId)
			if parentID != "" && !knownMessages[parentID] && !renderedQuotes[parentID] {
				renderedQuotes[parentID] = true
				body.WriteString(indent + "  [引用消息]\n")
				quoted, err := s.fetchMessage(ctx, parentID, state, fetchDepth+1)
				if err != nil {
					body.WriteString(indent + "    [引用消息不可读取，可能无会话权限或已被删除]\n")
				} else {
					writeIndentedText(&body, quoted.Text, indent+"    ")
					attachments = append(attachments, quoted.Attachments...)
				}
			}
			msgType := stringPtr(item.MsgType)
			content := mergeForwardMessageText(item)
			id := stringPtr(item.MessageId)
			if msgType == "merge_forward" && id != "" && !path[id] {
				body.WriteString(indent + "  [嵌套合并转发]\n")
				path[id] = true
				writeChildren(id, treeDepth+1)
				delete(path, id)
			} else {
				for _, line := range strings.Split(content, "\n") {
					fmt.Fprintf(&body, "%s  %s\n", indent, line)
				}
			}
			if item.Body != nil && item.Body.Content != nil {
				attachments = append(attachments, parseMessageAttachments(rootID, msgType, *item.Body.Content)...)
			}
		}
	}
	path[rootID] = true
	writeChildren(rootID, 0)

	header := fmt.Sprintf("[合并转发消息，共 %d 条", total)
	if rendered < total {
		header += fmt.Sprintf("，已展开前 %d 条", rendered)
	}
	header += "]\n"
	text := strings.TrimSpace(header + body.String())
	return truncateMergeForwardText(text), attachments
}

func writeIndentedText(body *strings.Builder, text, indent string) {
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		fmt.Fprintf(body, "%s%s\n", indent, line)
	}
}

func mergeForwardMessageHeader(item *larkim.Message) string {
	sender := "unknown"
	if item.Sender != nil && item.Sender.Id != nil && *item.Sender.Id != "" {
		sender = *item.Sender.Id
	}
	if ts := messageTimestamp(item); ts > 0 {
		return fmt.Sprintf("[%s | %s]", time.UnixMilli(ts).In(time.Local).Format("2006-01-02 15:04:05"), sender)
	}
	return "[" + sender + "]"
}

func mergeForwardMessageText(item *larkim.Message) string {
	msgType := stringPtr(item.MsgType)
	content := ""
	if item.Body != nil && item.Body.Content != nil {
		switch msgType {
		case "text", "post":
			content = parseMessageText(*item.Body.Content)
		case "interactive":
			content = parseInteractiveMessageText(*item.Body.Content)
		}
	}
	// 展开 `@_user_N` 占位符为「@Name(open_id)」——name 缺失时退到 open_id;
	// 只有 open_id 时给个 `@user(ou_xxx)` 让 agent 至少有个稳定 ID 可用。
	// 二者都缺失才保留原占位符,不破坏可读性。
	for _, mention := range item.Mentions {
		if mention == nil {
			continue
		}
		key := stringPtr(mention.Key)
		if key == "" {
			continue
		}
		replacement := formatMergeForwardMentionIdentity(mention)
		if replacement == "" {
			continue
		}
		content = strings.ReplaceAll(content, key, replacement)
	}
	if strings.TrimSpace(content) != "" && msgType != "merge_forward" {
		return strings.TrimSpace(content)
	}
	if msgType == "" {
		msgType = "unknown"
	}
	return "[" + msgType + " 消息]"
}

func formatMergeForwardMentionIdentity(mention *larkim.Mention) string {
	name := strings.TrimSpace(stringPtr(mention.Name))
	openID := strings.TrimSpace(stringPtr(mention.Id))
	switch {
	case name != "" && openID != "":
		return "@" + name + "(" + openID + ")"
	case openID != "":
		return "@user(" + openID + ")"
	case name != "":
		return "@" + name
	default:
		return ""
	}
}

// parseInteractiveMessageText extracts user-visible text from CardKit messages.
// Feishu wraps fetched cards in json_card and returns a normalized tree whose
// leaf text lives under property.elements. Cards before sending use the flatter
// element_id/content representation, so both forms are supported.
func parseInteractiveMessageText(raw string) string {
	var envelope map[string]any
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return ""
	}
	var card any = envelope
	if encoded, ok := envelope["json_card"].(string); ok && encoded != "" {
		if err := json.Unmarshal([]byte(encoded), &card); err != nil {
			return ""
		}
	}
	return strings.TrimSpace(renderCardText(card))
}

func renderCardText(value any) string {
	switch node := value.(type) {
	case string:
		return node
	case []any:
		return joinCardParts(node, "\n")
	case map[string]any:
		if skipCardNode(cardNodeID(node)) {
			return ""
		}
		tag := cardString(node, "tag")
		switch tag {
		case "br":
			return "\n"
		case "hr":
			return "\n---\n"
		case "standard_icon":
			return ""
		case "button":
			return renderCardButton(node)
		case "img", "image":
			return renderCardImage(node)
		case "plain_text", "code_span", "markdown", "md", "lark_md", "text":
			if content := cardContent(node); content != "" {
				return content
			}
			if rendered := joinCardParts(cardChildren(node, "elements"), ""); rendered != "" {
				return rendered
			}
			if text, ok := cardValue(node, "text"); ok {
				return renderCardText(text)
			}
			return ""
		case "link":
			return renderCardLink(node)
		case "list":
			return renderCardList(node, cardChildren(node, "items"))
		case "table":
			return renderCardTable(node)
		case "code_block":
			if content := cardContent(node); content != "" {
				return content
			}
			return joinCardParts(cardChildren(node, "contents"), "")
		}
		if tag == "" {
			if content := cardContent(node); content != "" {
				return content
			}
		}

		// Traverse only documented structural fields and in their display order.
		// This avoids leaking action payloads, configuration, or duplicated
		// markdownElements from the normalized CardKit response.
		parts := make([]string, 0)
		for _, key := range []string{"title", "header", "body", "newBody", "elements", "fields", "actions", "columns", "items", "contents", "text"} {
			if child, ok := cardValue(node, key); ok {
				separator := "\n"
				if key == "contents" || key == "text" {
					separator = ""
				}
				if list, ok := child.([]any); ok {
					if rendered := joinCardParts(list, separator); rendered != "" {
						parts = append(parts, rendered)
					}
				} else if rendered := renderCardText(child); rendered != "" {
					parts = append(parts, rendered)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}

// renderCardButton preserves button label + URL so agents can follow the
// link the user clearly intended to share. Feishu carries the URL under
// several keys depending on card generation ("url", "multi_url", or nested
// under "value"/"behaviors[].value.default_url").
func renderCardButton(node map[string]any) string {
	label := ""
	if text, ok := cardValue(node, "text"); ok {
		label = strings.TrimSpace(renderCardText(text))
	}
	if label == "" {
		label = cardString(node, "content")
	}
	url := cardExtractURL(node)
	switch {
	case label != "" && url != "":
		return fmt.Sprintf("[按钮:%s](%s)", label, url)
	case url != "":
		return fmt.Sprintf("[按钮](%s)", url)
	case label != "":
		return fmt.Sprintf("[按钮:%s]", label)
	}
	return ""
}

func renderCardLink(node map[string]any) string {
	label := ""
	if content := cardContent(node); content != "" {
		label = content
	} else if rendered := joinCardParts(cardChildren(node, "elements"), ""); rendered != "" {
		label = rendered
	} else if text, ok := cardValue(node, "text"); ok {
		label = strings.TrimSpace(renderCardText(text))
	}
	url := cardExtractURL(node)
	switch {
	case label != "" && url != "" && label != url:
		return fmt.Sprintf("[%s](%s)", label, url)
	case url != "":
		return url
	case label != "":
		return label
	}
	return ""
}

func renderCardImage(node map[string]any) string {
	key := cardImageKey(node)
	if key == "" {
		return "[图片]"
	}
	return "[图片 image_key=" + key + "]"
}

func cardImageKey(node map[string]any) string {
	if key := cardString(node, "image_key"); key != "" {
		return key
	}
	if property, ok := node["property"].(map[string]any); ok {
		if key := cardString(property, "image_key"); key != "" {
			return key
		}
	}
	return cardString(node, "img_key")
}

// cardExtractURL walks the documented URL-bearing fields on a button/link
// node. Order matters: multi_url wins because it carries platform-specific
// overrides (pc_url/ios_url/android_url) that are usually the true target.
func cardExtractURL(node map[string]any) string {
	if url := cardString(node, "url"); url != "" {
		return url
	}
	if url := cardStringFromMap(node, "multi_url", "url"); url != "" {
		return url
	}
	if url := cardStringFromMap(node, "multi_url", "pc_url"); url != "" {
		return url
	}
	if property, ok := node["property"].(map[string]any); ok {
		if url := cardString(property, "url"); url != "" {
			return url
		}
		if url := cardStringFromMap(property, "multi_url", "url"); url != "" {
			return url
		}
		if url := cardStringFromMap(property, "multi_url", "pc_url"); url != "" {
			return url
		}
	}
	if value, ok := node["value"].(map[string]any); ok {
		if url := cardString(value, "url"); url != "" {
			return url
		}
		if url := cardString(value, "default_url"); url != "" {
			return url
		}
	}
	for _, behavior := range cardChildren(node, "behaviors") {
		bnode, ok := behavior.(map[string]any)
		if !ok {
			continue
		}
		if url := cardString(bnode, "default_url"); url != "" {
			return url
		}
		if value, ok := bnode["value"].(map[string]any); ok {
			if url := cardString(value, "default_url"); url != "" {
				return url
			}
			if url := cardString(value, "url"); url != "" {
				return url
			}
		}
	}
	return ""
}

func cardStringFromMap(node map[string]any, key, inner string) string {
	child, ok := node[key].(map[string]any)
	if !ok {
		return ""
	}
	return cardString(child, inner)
}

func cardNodeID(node map[string]any) string {
	if id := cardString(node, "element_id"); id != "" {
		return id
	}
	return cardString(node, "id")
}

func skipCardNode(id string) bool {
	if id == "thought" || id == "tools" || id == "panel_thought" || id == "panel_tools" || id == "panel_process" || id == "card_title_actions" {
		return true
	}
	return strings.HasPrefix(id, "meta_") || strings.HasPrefix(id, "timeline_thought") || strings.HasPrefix(id, "timeline_tool")
}

func cardString(node map[string]any, key string) string {
	value, _ := node[key].(string)
	return value
}

func cardContent(node map[string]any) string {
	if content := cardString(node, "content"); content != "" {
		return content
	}
	if property, ok := node["property"].(map[string]any); ok {
		return cardString(property, "content")
	}
	return ""
}

func cardValue(node map[string]any, key string) (any, bool) {
	if value, ok := node[key]; ok {
		return value, true
	}
	if property, ok := node["property"].(map[string]any); ok {
		value, exists := property[key]
		return value, exists
	}
	return nil, false
}

func cardChildren(node map[string]any, key string) []any {
	value, ok := cardValue(node, key)
	if !ok {
		return nil
	}
	children, _ := value.([]any)
	return children
}

func joinCardParts(values []any, separator string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if rendered := renderCardText(value); rendered != "" {
			parts = append(parts, rendered)
		}
	}
	return strings.TrimSpace(strings.Join(parts, separator))
}

func renderCardList(list map[string]any, items []any) string {
	lines := make([]string, 0, len(items))
	orderedIndex := 0
	defaultType := cardString(list, "type")
	if property, ok := list["property"].(map[string]any); ok && defaultType == "" {
		defaultType = cardString(property, "type")
	}
	for _, item := range items {
		text := ""
		if node, ok := item.(map[string]any); ok {
			text = joinCardParts(cardChildren(node, "elements"), "")
		}
		if text == "" {
			text = renderCardText(item)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		prefix := "- "
		listType := defaultType
		if node, ok := item.(map[string]any); ok {
			if itemType := cardString(node, "type"); itemType != "" {
				listType = itemType
			}
			if property, ok := node["property"].(map[string]any); ok && listType == "" {
				listType = cardString(property, "type")
			}
		}
		if listType == "ordered" || listType == "number" {
			orderedIndex++
			prefix = fmt.Sprintf("%d. ", orderedIndex)
		}
		lines = append(lines, prefix+text)
	}
	return strings.Join(lines, "\n")
}

func renderCardTable(table map[string]any) string {
	columns := cardChildren(table, "columns")
	rows := cardChildren(table, "rows")
	if len(columns) == 0 || len(rows) == 0 {
		return ""
	}

	type column struct {
		name  string
		label string
	}
	ordered := make([]column, 0, len(columns))
	for _, raw := range columns {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := cardString(node, "name")
		if name == "" {
			continue
		}
		label := strings.TrimSpace(cardString(node, "display_name"))
		if label == "" {
			label = name
		}
		ordered = append(ordered, column{name: name, label: label})
	}
	if len(ordered) == 0 {
		return ""
	}

	var body strings.Builder
	body.WriteString("| ")
	for index, col := range ordered {
		if index > 0 {
			body.WriteString(" | ")
		}
		body.WriteString(escapeCardTableCell(col.label))
	}
	body.WriteString(" |\n|")
	for range ordered {
		body.WriteString(" --- |")
	}

	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		body.WriteString("\n| ")
		for index, col := range ordered {
			if index > 0 {
				body.WriteString(" | ")
			}
			body.WriteString(escapeCardTableCell(renderCardText(row[col.name])))
		}
		body.WriteString(" |")
	}
	return body.String()
}

func escapeCardTableCell(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "|", `\|`)
	value = strings.ReplaceAll(value, "\r\n", "<br>")
	value = strings.ReplaceAll(value, "\n", "<br>")
	return value
}

func messageTimestamp(item *larkim.Message) int64 {
	if item == nil || item.CreateTime == nil {
		return 0
	}
	var timestamp int64
	_, _ = fmt.Sscan(*item.CreateTime, &timestamp)
	return timestamp
}

func truncateMergeForwardText(text string) string {
	if utf8.RuneCountInString(text) <= maxMergeForwardTextRunes {
		return text
	}
	runes := []rune(text)
	marker := []rune("\n\n[合并转发内容过长，中间部分已省略]\n\n")
	available := maxMergeForwardTextRunes - len(marker)
	head := available * 3 / 8
	tail := available - head
	return string(runes[:head]) + string(marker) + string(runes[len(runes)-tail:])
}

func stringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
