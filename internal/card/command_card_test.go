package card

import "testing"

func TestCommandCardSectionElement(t *testing.T) {
	body := []map[string]any{
		markdownElement("body_1", "hello"),
		markdownElement("body_2", "world"),
	}
	section := sectionElement("分区标题", body)

	if section["tag"] == nil {
		t.Fatalf("section missing tag: %#v", section)
	}
	// Must have a grey rounded border for visual grouping.
	border, ok := section["border"].(map[string]string)
	if !ok {
		t.Fatalf("section border wrong type: %#v", section["border"])
	}
	if border["color"] != "grey" {
		t.Errorf("section border color = %q, want grey", border["color"])
	}
	if border["corner_radius"] == "" {
		t.Errorf("section border missing corner_radius")
	}
	// Must contain the body elements.
	elements, ok := section["elements"].([]map[string]any)
	if !ok {
		t.Fatalf("section elements wrong type: %#v", section["elements"])
	}
	if len(elements) != len(body) {
		t.Fatalf("section elements len = %d, want %d", len(elements), len(body))
	}
	if elements[0]["content"] != "hello" {
		t.Errorf("first body element content = %v, want hello", elements[0]["content"])
	}
}

func TestCommandCardFieldElements(t *testing.T) {
	control := configSelect("model", "opus", []string{"opus", "sonnet"})
	fields := fieldElements("Model", "仅对 Claude 生效", control)

	if len(fields) != 3 {
		t.Fatalf("fieldElements len = %d, want 3 (label+hint+control)", len(fields))
	}
	// Label is bold markdown.
	label := fields[0]
	if label["tag"] != "markdown" {
		t.Errorf("label tag = %v, want markdown", label["tag"])
	}
	if label["content"] != "**Model**" {
		t.Errorf("label content = %v, want **Model**", label["content"])
	}
	// Hint is a notation-sized grey markdown line.
	hint := fields[1]
	if hint["tag"] != "markdown" {
		t.Errorf("hint tag = %v, want markdown", hint["tag"])
	}
	if hint["text_size"] != "notation" {
		t.Errorf("hint text_size = %v, want notation", hint["text_size"])
	}
	if hint["content"] != "仅对 Claude 生效" {
		t.Errorf("hint content = %v, want the hint text", hint["content"])
	}
	// Control passed through.
	if fields[2]["tag"] != "select_static" {
		t.Errorf("control tag = %v, want select_static", fields[2]["tag"])
	}
}

func TestCommandCardFieldElementsEmptyHint(t *testing.T) {
	fields := fieldElements("Only label", "", nil)
	if len(fields) != 1 {
		t.Fatalf("fieldElements len = %d, want 1 (label only)", len(fields))
	}
	if fields[0]["content"] != "**Only label**" {
		t.Errorf("label content = %v", fields[0]["content"])
	}
}

func TestCommandCardFieldElementsHintOnly(t *testing.T) {
	fields := fieldElements("Label", "some hint", nil)
	if len(fields) != 2 {
		t.Fatalf("fieldElements len = %d, want 2 (label+hint)", len(fields))
	}
	if fields[1]["text_size"] != "notation" {
		t.Errorf("hint text_size = %v, want notation", fields[1]["text_size"])
	}
}

func TestCommandCardButtonRowElements(t *testing.T) {
	buttons := []Action{
		{ID: "save", Label: "保存", Value: "v1"},
		{ID: "close", Label: "关闭"},
	}
	row := buttonRowElements(buttons, "sess-123")

	tag, _ := row["tag"].(string)
	if tag != "column_set" {
		t.Fatalf("row tag = %q, want column_set", tag)
	}
	columns, ok := row["columns"].([]any)
	if !ok {
		t.Fatalf("row columns wrong type: %#v", row["columns"])
	}
	if len(columns) != 2 {
		t.Fatalf("row columns len = %d, want 2", len(columns))
	}

	// Each column should be equal-weighted.
	col0 := columns[0].(map[string]any)
	if col0["weight"] != 1 {
		t.Errorf("col0 weight = %v, want 1", col0["weight"])
	}

	btn0 := col0["elements"].([]any)[0].(map[string]any)
	if btn0["type"] != "primary" {
		t.Errorf("first button type = %v, want primary", btn0["type"])
	}
	if btn0["tag"] != "button" {
		t.Errorf("first button tag = %v, want button", btn0["tag"])
	}
	// Callback behavior carries the action + session.
	behaviors, ok := btn0["behaviors"].([]any)
	if !ok || len(behaviors) == 0 {
		t.Fatalf("first button behaviors wrong: %#v", btn0["behaviors"])
	}
	beh := behaviors[0].(map[string]any)
	val := beh["value"].(map[string]any)
	if val["action_id"] != "save" {
		t.Errorf("first button action_id = %v, want save", val["action_id"])
	}
	if val["session"] != "sess-123" {
		t.Errorf("first button session = %v, want sess-123", val["session"])
	}
	if val["value"] != "v1" {
		t.Errorf("first button value = %v, want v1", val["value"])
	}

	col1 := columns[1].(map[string]any)
	btn1 := col1["elements"].([]any)[0].(map[string]any)
	if btn1["type"] != "default" {
		t.Errorf("second button type = %v, want default", btn1["type"])
	}
}

func TestCommandCardButtonRowElementsEmpty(t *testing.T) {
	row := buttonRowElements(nil, "sess")
	if row != nil {
		t.Errorf("buttonRowElements(nil) = %#v, want nil", row)
	}
}

func TestCommandCardNoteElement(t *testing.T) {
	note := noteElement("note_1", "小灰字提示")
	if note["tag"] != "markdown" {
		t.Errorf("note tag = %v, want markdown", note["tag"])
	}
	if note["element_id"] != "note_1" {
		t.Errorf("note element_id = %v, want note_1", note["element_id"])
	}
	if note["content"] != "小灰字提示" {
		t.Errorf("note content = %v", note["content"])
	}
	if note["text_size"] != "notation" {
		t.Errorf("note text_size = %v, want notation", note["text_size"])
	}
}
