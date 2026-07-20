package bridge

import (
	"strings"
	"testing"
)

func TestHelpTextExplainsGroupMessageIntake(t *testing.T) {
	text := HelpText()
	for _, want := range []string{"mention only", "participated topics", "all group messages", "bot senders are ignored by default"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help text = %q, want %q", text, want)
		}
	}
}
