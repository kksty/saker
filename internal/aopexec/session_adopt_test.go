package aopexec

import (
	"os"
	"path/filepath"
	"testing"
)

// Adopting a replacement session keeps the node/url/agent identity and only
// replaces the session id and the stale turn/cursor: the case pointer must not
// start claiming a trajectory that was never opened.
func TestAdoptSessionForResumeRewritesPointer(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".saker"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := markerPath(dir)
	body := `{"node":"local","url":"http://hub:1","agent":"scan","session":"old","turn":"turn-1","cursor":"c9"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AdoptSessionForResume(dir, "new"); err != nil {
		t.Fatal(err)
	}
	info, ok := ReadSession(dir)
	if !ok {
		t.Fatal("adopted session is not readable")
	}
	if info.Session != "new" || info.URL != "http://hub:1" || info.Agent != "scan" {
		t.Fatalf("session pointer = %+v", info)
	}
	if info.Turn != "" {
		t.Fatalf("stale turn survived adoption: %q", info.Turn)
	}
	if !CanResume(dir) {
		t.Fatal("adopted case should be resumable")
	}
}

func TestAdoptSessionForResumeRequiresMarkerAndID(t *testing.T) {
	if err := AdoptSessionForResume(t.TempDir(), "new"); err == nil {
		t.Fatal("adopted a session for a case with no pointer")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".saker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath(dir), []byte(`{"url":"u","session":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AdoptSessionForResume(dir, ""); err == nil {
		t.Fatal("adopted an empty session id")
	}
}
