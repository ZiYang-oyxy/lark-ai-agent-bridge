package feishu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type captureGetMessageAPI struct {
	reqs        []*larkim.GetMessageReq
	resp        *larkim.GetMessageResp
	resps       []*larkim.GetMessageResp
	err         error
	errs        []error
	nilResponse bool
}

func (f *captureGetMessageAPI) Get(_ context.Context, req *larkim.GetMessageReq, _ ...larkcore.RequestOptionFunc) (*larkim.GetMessageResp, error) {
	index := len(f.reqs)
	f.reqs = append(f.reqs, req)
	if index < len(f.resps) {
		var err error
		if index < len(f.errs) {
			err = f.errs[index]
		}
		return f.resps[index], err
	}
	if f.nilResponse {
		return nil, f.err
	}
	return f.resp, f.err
}

func getMessageResp(msgType, content, senderID string) *larkim.GetMessageResp {
	item := &larkim.Message{}
	if msgType != "" {
		mt := msgType
		item.MsgType = &mt
	}
	if content != "" {
		c := content
		item.Body = &larkim.MessageBody{Content: &c}
	}
	if senderID != "" {
		sid := senderID
		item.Sender = &larkim.Sender{Id: &sid}
	}
	return &larkim.GetMessageResp{
		CodeError: larkcore.CodeError{Code: 0},
		Data:      &larkim.GetMessageRespData{Items: []*larkim.Message{item}},
	}
}

func TestSDKSenderFetchMessageText(t *testing.T) {
	api := &captureGetMessageAPI{resp: getMessageResp("text", `{"text":"我是一只黑猫"}`, "ou_author")}
	sender := &SDKSender{getAPI: api}
	got, err := sender.FetchMessage(t.Context(), "om_parent")
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "我是一只黑猫" {
		t.Fatalf("text = %q", got.Text)
	}
	if got.SenderID != "ou_author" {
		t.Fatalf("sender = %q", got.SenderID)
	}
	if got.MessageType != "text" {
		t.Fatalf("type = %q", got.MessageType)
	}
	if len(api.reqs) != 1 {
		t.Fatalf("expected exactly one Get call, got %d", len(api.reqs))
	}
}

// TestSDKSenderFetchMessagePropagatesSenderType 断言：飞书 Sender.SenderType（`user` /
// `app` / `anonymous` / `unknown`）不再被丢弃，会透传到 FetchedMessage.SenderType，
// 供上层 audit 和未来路由使用。
func TestSDKSenderFetchMessagePropagatesSenderType(t *testing.T) {
	msgType := "text"
	content := `{"text":"review subagent 已重新启动"}`
	senderID := "ou_other_bot"
	senderType := "app"
	item := &larkim.Message{
		MsgType: &msgType,
		Body:    &larkim.MessageBody{Content: &content},
		Sender:  &larkim.Sender{Id: &senderID, SenderType: &senderType},
	}
	api := &captureGetMessageAPI{resp: &larkim.GetMessageResp{
		CodeError: larkcore.CodeError{Code: 0},
		Data:      &larkim.GetMessageRespData{Items: []*larkim.Message{item}},
	}}
	sender := &SDKSender{getAPI: api}
	got, err := sender.FetchMessage(t.Context(), "om_parent")
	if err != nil {
		t.Fatal(err)
	}
	if got.SenderType != "app" {
		t.Fatalf("SenderType passthrough = %q, want app", got.SenderType)
	}
	if got.SenderID != "ou_other_bot" {
		t.Fatalf("SenderID = %q", got.SenderID)
	}
}

func TestSDKSenderFetchMessagePropagatesChatAndSenderName(t *testing.T) {
	msgType := "text"
	content := `{"text":"status"}`
	chatID := "oc_chat"
	senderID := "ou_author"
	senderName := "李俊Bot-Mike"
	item := &larkim.Message{
		ChatId:  &chatID,
		MsgType: &msgType,
		Body:    &larkim.MessageBody{Content: &content},
		Sender:  &larkim.Sender{Id: &senderID, SenderName: &senderName},
	}
	api := &captureGetMessageAPI{resp: &larkim.GetMessageResp{
		CodeError: larkcore.CodeError{Code: 0},
		Data:      &larkim.GetMessageRespData{Items: []*larkim.Message{item}},
	}}
	got, err := (&SDKSender{getAPI: api}).FetchMessage(t.Context(), "om_parent")
	if err != nil {
		t.Fatal(err)
	}
	if got.ChatID != chatID || got.SenderName != senderName {
		t.Fatalf("fetched identity = chat %q name %q", got.ChatID, got.SenderName)
	}
}

func TestSDKSenderFetchMessagePost(t *testing.T) {
	content := `{"title":"t","content":[[{"tag":"text","text":"line1"}],[{"tag":"text","text":"line2"}]]}`
	api := &captureGetMessageAPI{resp: getMessageResp("post", content, "ou_x")}
	sender := &SDKSender{getAPI: api}
	got, err := sender.FetchMessage(t.Context(), "om_parent")
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "line1\nline2" {
		t.Fatalf("post text = %q", got.Text)
	}
}

func TestSDKSenderFetchMessageExpandsMergeForwardTree(t *testing.T) {
	root := fetchedMessageItem("om_root", "", "merge_forward", "Merged and Forwarded Message", "ou_forwarder", "1710500000000")
	late := fetchedMessageItem("om_late", "om_root", "text", `{"text":"结论"}`, "ou_b", "1710500003000")
	nested := fetchedMessageItem("om_nested", "om_root", "merge_forward", "Merged and Forwarded Message", "ou_a", "1710500002000")
	early := fetchedMessageItem("om_early", "om_root", "text", `{"text":"你好 @_user_1"}`, "ou_a", "1710500001000")
	key, id, name := "@_user_1", "ou_b", "李四"
	early.Mentions = []*larkim.Mention{{Key: &key, Id: &id, Name: &name}}
	image := fetchedMessageItem("om_image", "om_nested", "image", `{"image_key":"img_x"}`, "ou_b", "1710500002500")

	api := &captureGetMessageAPI{resp: &larkim.GetMessageResp{
		CodeError: larkcore.CodeError{Code: 0},
		Data:      &larkim.GetMessageRespData{Items: []*larkim.Message{root, late, image, nested, early}},
	}}
	got, err := (&SDKSender{getAPI: api}).FetchMessage(t.Context(), "om_root")
	if err != nil {
		t.Fatal(err)
	}
	if got.MessageType != "merge_forward" || got.SenderID != "ou_forwarder" {
		t.Fatalf("metadata = type:%q sender:%q", got.MessageType, got.SenderID)
	}
	// 合并转发子消息里的 `@_user_N` 展开成 `@Name(open_id)`,让 agent 既知道被
	// @ 的显示名,也拿到稳定的 open_id 便于后续操作。
	for _, want := range []string{"[合并转发消息，共 4 条]", "你好 @李四(ou_b)", "[嵌套合并转发]", "[image 消息]", "结论"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("expanded text missing %q:\n%s", want, got.Text)
		}
	}
	if strings.Contains(got.Text, "@_user_1") {
		t.Fatalf("mention placeholder must be expanded, got:\n%s", got.Text)
	}
	if earlyAt, nestedAt, lateAt := strings.Index(got.Text, "你好"), strings.Index(got.Text, "[嵌套合并转发]"), strings.Index(got.Text, "结论"); !(earlyAt < nestedAt && nestedAt < lateAt) {
		t.Fatalf("messages are not chronological: early=%d nested=%d late=%d\n%s", earlyAt, nestedAt, lateAt, got.Text)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].MessageID != "om_root" || got.Attachments[0].FileKey != "img_x" {
		t.Fatalf("attachments = %#v", got.Attachments)
	}
}

// TestSDKSenderFetchMessageMergeForwardMentionFallbackToOpenID 锁死 name 缺失时的
// 退化路径:只要 mention 携带 open_id,子消息里的占位符就必须换成 `@user(open_id)`,
// 而不是把裸 `@_user_N` 透给 agent。飞书外部人员 mention 常常没有 Name。
func TestSDKSenderFetchMessageMergeForwardMentionFallbackToOpenID(t *testing.T) {
	root := fetchedMessageItem("om_root2", "", "merge_forward", "Merged and Forwarded Message", "ou_forwarder", "1710500000000")
	child := fetchedMessageItem("om_child2", "om_root2", "text", `{"text":"提醒 @_user_9 参加"}`, "ou_c", "1710500001000")
	key, id := "@_user_9", "ou_stranger"
	child.Mentions = []*larkim.Mention{{Key: &key, Id: &id}}

	api := &captureGetMessageAPI{resp: &larkim.GetMessageResp{
		CodeError: larkcore.CodeError{Code: 0},
		Data:      &larkim.GetMessageRespData{Items: []*larkim.Message{root, child}},
	}}
	got, err := (&SDKSender{getAPI: api}).FetchMessage(t.Context(), "om_root2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "提醒 @user(ou_stranger) 参加") {
		t.Fatalf("open_id fallback missing:\n%s", got.Text)
	}
	if strings.Contains(got.Text, "@_user_9") {
		t.Fatalf("placeholder must be replaced:\n%s", got.Text)
	}
}

func TestSDKSenderFetchMessageExpandsMissingQuotedParent(t *testing.T) {
	outerRoot := fetchedMessageItem("om_outer", "", "merge_forward", "Merged and Forwarded Message", "ou_f", "1")
	reply := fetchedMessageItem("om_reply", "om_outer", "text", `{"text":"外层回复"}`, "ou_a", "3")
	parentID := "om_inner"
	reply.ParentId = &parentID

	innerRoot := fetchedMessageItem(parentID, "", "merge_forward", "Merged and Forwarded Message", "ou_f", "1")
	innerText := fetchedMessageItem("om_inner_text", parentID, "text", `{"text":"INNER-QUOTE-731"}`, "ou_b", "2")
	api := &captureGetMessageAPI{resps: []*larkim.GetMessageResp{
		messageItemsResp(outerRoot, reply),
		messageItemsResp(innerRoot, innerText),
	}}

	got, err := (&SDKSender{getAPI: api}).FetchMessage(t.Context(), "om_outer")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[引用消息]", "INNER-QUOTE-731", "外层回复"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("expanded text missing %q:\n%s", want, got.Text)
		}
	}
	if len(api.reqs) != 2 {
		t.Fatalf("Get calls = %d, want 2", len(api.reqs))
	}
}

func TestSDKSenderFetchMessageMarksInaccessibleQuotedParent(t *testing.T) {
	outerRoot := fetchedMessageItem("om_outer", "", "merge_forward", "Merged and Forwarded Message", "ou_f", "1")
	first := fetchedMessageItem("om_first", "om_outer", "text", `{"text":"第一条"}`, "ou_a", "2")
	second := fetchedMessageItem("om_second", "om_outer", "text", `{"text":"第二条"}`, "ou_b", "3")
	parentID := "om_inaccessible"
	first.ParentId = &parentID
	second.ParentId = &parentID
	denied := &larkim.GetMessageResp{CodeError: larkcore.CodeError{Code: 230002, Msg: "outside chat"}}
	api := &captureGetMessageAPI{resps: []*larkim.GetMessageResp{
		messageItemsResp(outerRoot, first, second),
		denied,
	}}

	got, err := (&SDKSender{getAPI: api}).FetchMessage(t.Context(), "om_outer")
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(got.Text, "引用消息不可读取"); count != 1 {
		t.Fatalf("unavailable quote markers = %d, want 1:\n%s", count, got.Text)
	}
	if len(api.reqs) != 2 {
		t.Fatalf("Get calls = %d, want cached 2", len(api.reqs))
	}
}

func TestSDKSenderFetchMessageExtractsInteractiveCardText(t *testing.T) {
	card := `{"body":{"property":{"elements":[{"id":"panel_thought","tag":"collapsible_panel","property":{"elements":[{"id":"thought","tag":"markdown","property":{"elements":[{"tag":"plain_text","property":{"content":"隐藏思考"}}]}}]}},{"id":"answer","tag":"markdown","property":{"elements":[{"tag":"plain_text","property":{"content":"结论："}},{"tag":"code_span","property":{"content":"state root"}},{"tag":"br"},{"tag":"list","property":{"items":[{"type":"bullet","elements":[{"tag":"plain_text","property":{"content":"保留配置"}}]},{"type":"ordered","elements":[{"tag":"plain_text","property":{"content":"执行迁移"}}]}]}}]}},{"id":"panel_tools","tag":"collapsible_panel","property":{"elements":[{"id":"tools","tag":"markdown","content":"工具过程"}]}},{"id":"meta_primary","tag":"markdown","content":"运行元数据"}]}}}`
	interactive := fetchedMessageItem("om_card", "om_root", "interactive", fmt.Sprintf(`{"json_card":%q}`, card), "ou_bot", "1710500002000")
	root := fetchedMessageItem("om_root", "", "merge_forward", "Merged and Forwarded Message", "ou_forwarder", "1710500000000")
	api := &captureGetMessageAPI{resp: &larkim.GetMessageResp{
		CodeError: larkcore.CodeError{Code: 0},
		Data:      &larkim.GetMessageRespData{Items: []*larkim.Message{root, interactive}},
	}}
	got, err := (&SDKSender{getAPI: api}).FetchMessage(t.Context(), "om_root")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"结论：state root", "- 保留配置", "1. 执行迁移"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("interactive text missing %q:\n%s", want, got.Text)
		}
	}
	for _, hidden := range []string{"[interactive 消息]", "隐藏思考", "工具过程", "运行元数据"} {
		if strings.Contains(got.Text, hidden) {
			t.Fatalf("interactive text contains hidden content %q:\n%s", hidden, got.Text)
		}
	}
}

func TestParseInteractiveMessageTextSupportsFlatCard(t *testing.T) {
	raw := `{"json_card":"{\"body\":{\"elements\":[{\"tag\":\"markdown\",\"element_id\":\"answer\",\"content\":\"桥接器正文\"}]}}"}`
	if got := parseInteractiveMessageText(raw); got != "桥接器正文" {
		t.Fatalf("text = %q", got)
	}
}

func TestParseInteractiveMessageTextSupportsLegacyCard(t *testing.T) {
	raw := `{"title":"旧卡片","elements":[[{"tag":"img","image_key":"img_x"},{"tag":"text","text":"请升级至最新版本客户端，以查看内容"}]]}`
	got := parseInteractiveMessageText(raw)
	for _, want := range []string{"旧卡片", "[图片 image_key=img_x]", "请升级至最新版本客户端，以查看内容"} {
		if !strings.Contains(got, want) {
			t.Fatalf("legacy card text missing %q: %q", want, got)
		}
	}
}

func TestParseInteractiveMessageTextSupportsLegacyFieldsAndActions(t *testing.T) {
	raw := `{"header":{"title":{"tag":"lark_md","content":"测试失败!!!"}},"elements":[{"tag":"div","fields":[{"is_short":true,"text":{"tag":"lark_md","content":"**任务名称**"}},{"is_short":true,"text":{"tag":"lark_md","content":"RNIC 版本回归测试(HAPS)"}}]},{"tag":"div","text":{"tag":"lark_md","content":"**报告链接**"}},{"tag":"action","actions":[{"tag":"button","text":{"tag":"lark_md","content":"**测试报告**"},"behaviors":[{"type":"open_url","default_url":"https://reports.example.com/rnic"}]}]}]}`
	got := parseInteractiveMessageText(raw)
	for _, want := range []string{
		"测试失败!!!",
		"**任务名称**",
		"RNIC 版本回归测试(HAPS)",
		"**报告链接**",
		"[按钮:**测试报告**](https://reports.example.com/rnic)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("legacy card text missing %q:\n%s", want, got)
		}
	}
}

func TestSDKSenderFetchMessageRendersInteractiveTable(t *testing.T) {
	content := `{"header":{"title":{"tag":"plain_text","content":"测试失败!!!"}},"elements":[{"tag":"button","text":{"tag":"plain_text","content":"测试报告"},"url":"https://reports.example.com/rnic"},{"tag":"table","columns":[{"name":"A","display_name":"No."},{"name":"B","display_name":"用例名称"},{"name":"C","display_name":"结果"}],"rows":[{"A":{"tag":"plain_text","content":"1"},"B":{"tag":"plain_text","content":"case|one"},"C":{"tag":"plain_text","content":"failed"}},{"A":{"tag":"plain_text","content":"2"},"B":{"tag":"plain_text","content":"case two"},"C":{"tag":"plain_text","content":"passed"}}]}]}`
	api := &captureGetMessageAPI{resp: getMessageResp("interactive", content, "ou_reporter")}
	got, err := (&SDKSender{getAPI: api}).FetchMessage(t.Context(), "om_card")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"测试失败!!!",
		"[按钮:测试报告](https://reports.example.com/rnic)",
		"| No. | 用例名称 | 结果 |",
		`| 1 | case\|one | failed |`,
		"| 2 | case two | passed |",
	} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("interactive text missing %q:\n%s", want, got.Text)
		}
	}
	if strings.Contains(got.Text, `"tag":"table"`) {
		t.Fatalf("interactive card leaked raw JSON instead of rendered text:\n%s", got.Text)
	}
}

func TestParseInteractiveMessageTextPreservesButtonURL(t *testing.T) {
	// Legacy card carrying a primary button with an explicit URL, as Feishu
	// delivers when the quoted card was sent via the incoming webhook API.
	raw := `{"title":"测试失败!!!","elements":[[{"tag":"text","text":"报告链接"}],[{"tag":"button","text":{"tag":"plain_text","content":"测试报告"},"type":"primary","url":"https://reports.example.com/rnic/2026-07-27","value":{"url":"https://reports.example.com/rnic/2026-07-27"}}]]}`
	got := parseInteractiveMessageText(raw)
	if !strings.Contains(got, "测试报告") {
		t.Fatalf("button label missing: %q", got)
	}
	if !strings.Contains(got, "https://reports.example.com/rnic/2026-07-27") {
		t.Fatalf("button url missing: %q", got)
	}
}

func TestParseInteractiveMessageTextPreservesButtonMultiURL(t *testing.T) {
	// CardKit v2 button using multi_url (pc/ios/android variants).
	raw := `{"json_card":"{\"body\":{\"elements\":[{\"tag\":\"button\",\"text\":{\"tag\":\"plain_text\",\"content\":\"打开报告\"},\"multi_url\":{\"url\":\"https://reports.example.com/x\",\"pc_url\":\"https://reports.example.com/x?pc=1\"}}]}}"}`
	got := parseInteractiveMessageText(raw)
	if !strings.Contains(got, "打开报告") {
		t.Fatalf("button label missing: %q", got)
	}
	if !strings.Contains(got, "https://reports.example.com/x") {
		t.Fatalf("button url missing: %q", got)
	}
}

func TestSDKSenderFetchMessageInteractiveKeepsButtonURL(t *testing.T) {
	// End-to-end: SDKSender.FetchMessage on a fetched interactive card must
	// surface the button URL so it reaches the agent prompt.
	content := `{"title":"测试失败!!!","elements":[[{"tag":"text","text":"报告链接"}],[{"tag":"button","text":{"tag":"plain_text","content":"测试报告"},"type":"primary","url":"https://reports.example.com/rnic"}]]}`
	api := &captureGetMessageAPI{resp: getMessageResp("interactive", content, "ou_reporter")}
	got, err := (&SDKSender{getAPI: api}).FetchMessage(t.Context(), "om_card")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "测试报告") {
		t.Fatalf("card button label missing: %q", got.Text)
	}
	if !strings.Contains(got.Text, "https://reports.example.com/rnic") {
		t.Fatalf("card button url missing: %q", got.Text)
	}
}

func TestParseInteractiveMessageTextPreservesLinkURL(t *testing.T) {
	raw := `{"json_card":"{\"body\":{\"elements\":[{\"tag\":\"link\",\"url\":\"https://reports.example.com/detail\",\"content\":\"查看详情\"}]}}"}`
	got := parseInteractiveMessageText(raw)
	if !strings.Contains(got, "查看详情") {
		t.Fatalf("link label missing: %q", got)
	}
	if !strings.Contains(got, "https://reports.example.com/detail") {
		t.Fatalf("link url missing: %q", got)
	}
}

func TestSDKSenderFetchMessageBoundsMergeForwardContext(t *testing.T) {
	items := []*larkim.Message{fetchedMessageItem("om_root", "", "merge_forward", "Merged and Forwarded Message", "ou_f", "1")}
	for i := 0; i < maxMergeForwardMessages+5; i++ {
		items = append(items, fetchedMessageItem(
			fmt.Sprintf("om_%03d", i), "om_root", "text",
			fmt.Sprintf(`{"text":"%03d %s"}`, i, strings.Repeat("长", 500)),
			"ou_a", fmt.Sprint(i+2),
		))
	}
	api := &captureGetMessageAPI{resp: &larkim.GetMessageResp{
		CodeError: larkcore.CodeError{Code: 0},
		Data:      &larkim.GetMessageRespData{Items: items},
	}}
	got, err := (&SDKSender{getAPI: api}).FetchMessage(t.Context(), "om_root")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "共 205 条，已展开前 200 条") {
		t.Fatalf("missing message limit marker: %s", got.Text[:min(len(got.Text), 200)])
	}
	if !strings.Contains(got.Text, "中间部分已省略") {
		t.Fatal("missing rune truncation marker")
	}
	if runes := utf8.RuneCountInString(got.Text); runes != maxMergeForwardTextRunes {
		t.Fatalf("text runes = %d, want %d", runes, maxMergeForwardTextRunes)
	}
}

func fetchedMessageItem(id, upper, msgType, content, sender, createTime string) *larkim.Message {
	item := &larkim.Message{}
	item.MessageId = &id
	if upper != "" {
		item.UpperMessageId = &upper
	}
	item.MsgType = &msgType
	item.CreateTime = &createTime
	item.Body = &larkim.MessageBody{Content: &content}
	item.Sender = &larkim.Sender{Id: &sender}
	return item
}

func messageItemsResp(items ...*larkim.Message) *larkim.GetMessageResp {
	return &larkim.GetMessageResp{
		CodeError: larkcore.CodeError{Code: 0},
		Data:      &larkim.GetMessageRespData{Items: items},
	}
}

func TestSDKSenderFetchMessageEmptyID(t *testing.T) {
	sender := &SDKSender{getAPI: &captureGetMessageAPI{}}
	if _, err := sender.FetchMessage(t.Context(), ""); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestSDKSenderFetchMessageAPIError(t *testing.T) {
	api := &captureGetMessageAPI{nilResponse: true, err: errors.New("boom")}
	sender := &SDKSender{getAPI: api}
	if _, err := sender.FetchMessage(t.Context(), "om_parent"); err == nil {
		t.Fatal("expected error propagated from API")
	}
}

func TestSDKSenderFetchMessageNoItems(t *testing.T) {
	api := &captureGetMessageAPI{resp: &larkim.GetMessageResp{
		CodeError: larkcore.CodeError{Code: 0},
		Data:      &larkim.GetMessageRespData{Items: nil},
	}}
	sender := &SDKSender{getAPI: api}
	if _, err := sender.FetchMessage(t.Context(), "om_parent"); err == nil {
		t.Fatal("expected error when no items returned")
	}
}
