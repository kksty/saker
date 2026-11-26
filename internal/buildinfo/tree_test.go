package buildinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The freshness decision is the part of the gate a unit test can prove without
// touching the real frontend: a rebuild that changes nothing passes, and exactly
// the changed paths are reported when the embedded product is stale.
func TestDiffTreeSnapshotsDetectsStaleEmbeddedArtifacts(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "<html>old</html>")
	write("assets/app.js", "console.log(1)\n")

	before, err := SnapshotTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Exists || len(before.Files) != 2 {
		t.Fatalf("snapshot = %+v", before)
	}

	// A rebuild that produces identical bytes (vite rewrites unchanged files and
	// only the mtime moves) must not be reported as a difference.
	if got := DiffTreeSnapshots(before, before); len(got) != 0 {
		t.Fatalf("identical snapshot diff = %v", got)
	}
	write("assets/app.js", "console.log(1)\n")
	unchanged, err := SnapshotTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := DiffTreeSnapshots(before, unchanged); len(got) != 0 {
		t.Fatalf("rewritten-but-identical tree diff = %v", got)
	}

	// A stale embedded product: the source build changed one file, replaced
	// another and added a third.
	write("index.html", "<html>new</html>")
	write("assets/app.js", "console.log(2)\n")
	write("assets/new.css", "body{}\n")
	after, err := SnapshotTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	changed := DiffTreeSnapshots(before, after)
	want := []string{"assets/app.js", "assets/new.css", "index.html"}
	if strings.Join(changed, ",") != strings.Join(want, ",") {
		t.Fatalf("changed = %v, want %v", changed, want)
	}
	if before.AggregateSHA256() == after.AggregateSHA256() {
		t.Fatal("aggregate digest did not change")
	}
	if msg := FormatChangedPaths(changed, 1); !strings.Contains(msg, "assets/app.js") || !strings.Contains(msg, "等 3 个文件") {
		t.Fatalf("FormatChangedPaths = %q", msg)
	}
}

func TestSnapshotTreeMissingDirIsEmpty(t *testing.T) {
	snap, err := SnapshotTree(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Exists || len(snap.Files) != 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.AggregateSHA256() == "" {
		t.Fatal("empty snapshot digest is empty")
	}
}

func TestAggregateSHA256IsOrderIndependent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.js"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.js"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := SnapshotTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SnapshotTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.AggregateSHA256() != second.AggregateSHA256() {
		t.Fatal("same tree produced two aggregates")
	}
	if len(first.Paths()) != 2 || first.Paths()[0] != "a.js" {
		t.Fatalf("paths = %v", first.Paths())
	}
}

func TestSanitizeBaseURLDropsCredentialsAndQuery(t *testing.T) {
	cases := []struct {
		raw  string
		host string
		path string
	}{
		{"https://api.example.test/v1", "https://api.example.test", "/v1"},
		{"http://user:secret@127.0.0.1:3425/v1?api_key=leak#frag", "http://127.0.0.1:3425", "/v1"},
		{"https://gateway.test/route/", "https://gateway.test", "/route/"},
	}
	for _, tc := range cases {
		host, path, err := sanitizeBaseURL(tc.raw)
		if err != nil {
			t.Fatalf("%q: %v", tc.raw, err)
		}
		if host != tc.host || path != tc.path {
			t.Fatalf("%q -> %q %q, want %q %q", tc.raw, host, path, tc.host, tc.path)
		}
		if strings.Contains(host+path, "secret") || strings.Contains(host+path, "api_key") {
			t.Fatalf("%q kept credentials in %q", tc.raw, host+path)
		}
	}
	if _, _, err := sanitizeBaseURL("not a url"); err == nil {
		t.Fatal("invalid base_url accepted")
	}
}

func TestRedactSecretsRemovesRawAndEscapedForms(t *testing.T) {
	const key = "sk-live-0123456789abcdef"
	text := `bad key ` + key + ` and escaped \"` + key + `\" and "` + key + `"`
	got := redactSecrets(text, []string{"", key, "unrelated"})
	if strings.Contains(got, key) {
		t.Fatalf("key survived redaction: %q", got)
	}
	if !strings.Contains(got, "***") {
		t.Fatalf("no placeholder in %q", got)
	}
}

func TestHarnessCloneHintMentionsBothPaths(t *testing.T) {
	hint := HarnessCloneHint("/repo")
	if !strings.Contains(hint, "saker/fixes") || !strings.Contains(hint, "HARNESS_DIR") {
		t.Fatalf("hint = %q", hint)
	}
}
