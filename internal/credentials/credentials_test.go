package credentials

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUsesSecureSecretFiles(t *testing.T) {
	dir := t.TempDir()
	idPath := filepath.Join(dir, "app-id")
	secretPath := filepath.Join(dir, "app-secret")
	for path, value := range map[string]string{idPath: "cli_file\n", secretPath: "file-secret\r\n"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("LARK_APP_ID_FILE", idPath)
	t.Setenv("LARK_APP_SECRET_FILE", secretPath)
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.AppID != "cli_file" || got.AppSecret != "file-secret" {
		t.Fatalf("credentials = %#v", got)
	}
}

func TestLoadDirectValuesTakePrecedence(t *testing.T) {
	t.Setenv("LAB_LARK_APP_ID", "cli_direct")
	t.Setenv("LAB_LARK_APP_SECRET", "direct-secret")
	t.Setenv("LARK_APP_ID_FILE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("LARK_APP_SECRET_FILE", filepath.Join(t.TempDir(), "missing"))
	got, err := Load()
	if err != nil || got.AppID != "cli_direct" || got.AppSecret != "direct-secret" {
		t.Fatalf("credentials = %#v, %v", got, err)
	}
}

func TestLoadRejectsInsecureSecretFileWithoutLeakingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	const secret = "must-not-leak"
	if err := os.WriteFile(path, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LARK_APP_ID", "cli_test")
	t.Setenv("LARK_APP_SECRET_FILE", path)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "permissions") || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %v", err)
	}
}
