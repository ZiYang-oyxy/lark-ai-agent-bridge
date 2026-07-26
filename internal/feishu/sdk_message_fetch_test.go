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
	for _, want := range []string{"[合并转发消息，共 4 条]", "你好 @李四", "[嵌套合并转发]", "[image 消息]", "结论"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("expanded text missing %q:\n%s", want, got.Text)
		}
	}
	if earlyAt, nestedAt, lateAt := strings.Index(got.Text, "你好"), strings.Index(got.Text, "[嵌套合并转发]"), strings.Index(got.Text, "结论"); !(earlyAt < nestedAt && nestedAt < lateAt) {
		t.Fatalf("messages are not chronological: early=%d nested=%d late=%d\n%s", earlyAt, nestedAt, lateAt, got.Text)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].MessageID != "om_root" || got.Attachments[0].FileKey != "img_x" {
		t.Fatalf("attachments = %#v", got.Attachments)
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
	for _, want := range []string{"旧卡片", "[图片]", "请升级至最新版本客户端，以查看内容"} {
		if !strings.Contains(got, want) {
			t.Fatalf("legacy card text missing %q: %q", want, got)
		}
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
