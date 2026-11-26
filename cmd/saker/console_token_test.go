package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The startup line names the file, never the secret. consoleTokenNotice is the
// only path from a freshly generated token to stdout, and it does not receive
// the value at all — this test pins the shape of what it prints.
func TestConsoleTokenNoticeCarriesPathNotSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "console.token")
	value := strings.Repeat("9e0f", 12) // 48 hex chars, shaped like a console token
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	notice := consoleTokenNotice(path)
	if !strings.Contains(notice, path) {
		t.Fatalf("notice does not say where the token is: %q", notice)
	}
	if strings.Contains(notice, value) {
		t.Fatalf("notice leaks the token value: %q", notice)
	}
}
