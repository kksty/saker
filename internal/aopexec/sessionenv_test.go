package aopexec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/aop"
)

func TestSessionToolEnvMatchesCaseState(t *testing.T) {
	dir := t.TempDir()
	sakerDir := filepath.Join(dir, ".saker")
	if err := os.MkdirAll(sakerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sakerDir, "identity.json"), []byte(
		`{"api_base":"http://127.0.0.1:8080/","project_id":"p-1","intent_id":"in-9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sakerDir, "auth"),
		[]byte("Authorization: Bearer tok-xyz\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	env := sessionToolEnv(dir)
	want := map[string]string{
		"SAKER_API":                    "http://127.0.0.1:8080",
		"SAKER_PROJECT":                "p-1",
		"SAKER_SELF":                   "in-9",
		"SAKER_AGENT":                  "http://127.0.0.1:8080/agent/p-1/api",
		"SAKER_BATTLE":                 "http://127.0.0.1:8080/p/p-1/api",
		"SAKER_AUTH":                   "Authorization: Bearer tok-xyz",
		aop.DisableNativeIOASendEnv: "1",
	}
	for key, value := range want {
		if env[key] != value {
			t.Fatalf("%s = %q, want %q", key, env[key], value)
		}
	}
	if env["SAKER_BIN"] == "" {
		t.Fatal("SAKER_BIN is empty; the shell cannot call the native read verbs")
	}
}

func TestSessionToolEnvIsNilWithoutIdentity(t *testing.T) {
	// A case with no identity for this turn must yield nil, not an environment
	// with empty values the model could mistake for real ones.
	if env := sessionToolEnv(t.TempDir()); env != nil {
		t.Fatalf("missing identity produced %+v", env)
	}
	if env := sessionToolEnv(""); env != nil {
		t.Fatalf("empty dir produced %+v", env)
	}
}
