package card

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildConfigCardRendersGroupModeAndBotSwitch(t *testing.T) {
	payload := BuildLarkCard(Event{Type: "config", SessionID: "config", ConfigForm: &ConfigForm{
		GroupMessageMode: "participated_topics",
		RespondToBots:    "false",
	}})
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`"name":"group_message_mode"`, `"initial_option":"participated_topics"`,
		`"value":"mention_only"`, `"value":"participated_topics"`, `"value":"all_group_messages"`,
		`"name":"respond_to_bots"`, `"value":"false"`, `"value":"true"`,
		"im:message.group_msg", "Bridge 自身消息始终忽略",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config card missing %q: %s", want, text)
		}
	}
}
