package security

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	in := "Authorization: Bearer abc.def token=secret-value password:123"
	out := Redact(in)
	if strings.Contains(out, "abc.def") || strings.Contains(out, "secret-value") || strings.Contains(out, "123") {
		t.Fatalf("secret leaked in %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("redaction marker missing in %q", out)
	}
}
