package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// findSessionBash returns the POSIX shell the harness host would use. On Windows
// the first `bash` on PATH is often WSL's (it cannot see the Windows working
// directory), so prefer Git's bash and reject a Linux kernel.
func findSessionBash(t *testing.T) (string, string) {
	t.Helper()
	candidates := []string{}
	if p, err := exec.LookPath("bash"); err == nil {
		candidates = append(candidates, p)
	}
	if runtime.GOOS == "windows" {
		for _, p := range []string{
			`C:\Program Files\Git\usr\bin\bash.exe`,
			`C:\Program Files\Git\bin\bash.exe`,
			`C:\Program Files (x86)\Git\usr\bin\bash.exe`,
		} {
			candidates = append(candidates, p)
		}
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		out, err := exec.Command(p, "-c", "uname -s").Output()
		if err != nil {
			continue
		}
		if runtime.GOOS == "windows" && strings.Contains(strings.ToLower(string(out)), "linux") {
			continue // WSL: /mnt/c paths, not the session case
		}
		// Git's POSIX tools (sed, dirname, cat) live next to usr\bin\bash.exe.
		dir := filepath.Dir(p)
		if filepath.Base(dir) == "bin" {
			if sib := filepath.Join(filepath.Dir(dir), "usr", "bin"); fileExists(sib) {
				dir = sib
			}
		}
		return p, dir
	}
	t.Skip("no Git-style bash available: the harness host provides one")
	return "", ""
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// The shell init is how a session gets $SAKER_AUTH/$SAKER_BATTLE without reading or
// exporting anything. It must resolve the caller's case from the working
// directory (including a subdirectory) and carry no secret of its own.
//
// Scope: this exercises the script through a real bash, which honors BASH_ENV.
// It does NOT represent the production path: the harness runs commands in an
// embedded POSIX sh that never reads BASH_ENV, so the script is not sourced
// there and the session's $SAKER_* stay empty. Product coverage of that path has to
// run through the harness bash tool with an injected environment; this test only
// pins the script's own logic.
func TestSessionEnvScriptExportsTheCallersCredential(t *testing.T) {
	bash, toolsDir := findSessionBash(t)
	data := t.TempDir()
	envPath, err := writeSessionEnv(data)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(data, "projects", "p1", "cases", "in_1")
	if err := os.MkdirAll(filepath.Join(work, ".tw"), 0o755); err != nil {
		t.Fatal(err)
	}
	identity := `{"token":"tok-123","project_id":"p1","intent_id":"in_1","run_id":"r1","api_base":"http://127.0.0.1:9000"}`
	if err := os.WriteFile(filepath.Join(work, ".tw", "identity.json"), []byte(identity), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".tw", "auth"), []byte("Authorization: Bearer tok-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "raw"), 0o755); err != nil {
		t.Fatal(err)
	}

	read := func(dir string) []string {
		t.Helper()
		cmd := exec.Command(bash, "-c", `printf '%s\n%s\n%s\n%s\n' "$SAKER_AUTH" "$SAKER_AGENT" "$SAKER_BATTLE" "$SAKER_SELF"`)
		cmd.Dir = dir
		// A minimal env: BASH_ENV plus PATH (sed/dirname/cat come from Git).
		cmd.Env = []string{
			"PATH=" + toolsDir + string(os.PathListSeparator) + os.Getenv("PATH"),
			"BASH_ENV=" + filepath.ToSlash(envPath),
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("bash with BASH_ENV: %v", err)
		}
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}

	want := []string{
		"Authorization: Bearer tok-123",
		"http://127.0.0.1:9000/agent/p1/api",
		"http://127.0.0.1:9000/p/p1/api",
		"in_1",
	}
	for _, dir := range []string{work, filepath.Join(work, "raw")} {
		got := read(dir)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("from %s: got %q, want %q", dir, got, want)
		}
	}

	// The script itself carries no token; the credential comes from the case.
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tok-123") {
		t.Fatal("session env script embeds a credential")
	}
}

// Reinstalling an unchanged script must not churn the file.
func TestWriteSessionEnvIsIdempotent(t *testing.T) {
	data := t.TempDir()
	first, err := writeSessionEnv(data)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := writeSessionEnv(data)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("unchanged script was rewritten: %s -> %s", before.ModTime(), after.ModTime())
	}
}
