package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A failed manifest write inside create used to return only the error: the
// ledger handle stayed open and the half-made directory stayed on disk. The
// next start then reported it as a skipped project that cannot be repaired from
// the console (no project.json is silently rewritten by load, but the skip is
// permanent until someone removes the tree by hand).
//
// The failure is injected through createWith's writer, because the only
// deterministic disk failure here (a blocked project.json.tmp) would have to
// exist before the random id is known.
func TestCreateManifestWriteFailureLeavesNothingBehind(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("disk full (injected)")
	p, err := m.createWith("p-broken", "fixture", "scope", func(*Project) error { return boom })
	if err == nil {
		t.Fatal("a failed manifest write reported a created project")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error %v does not wrap the original failure", err)
	}
	if p != nil {
		t.Fatalf("create returned a project (%s) after its manifest write failed", p.Dir)
	}
	if m.Get("p-broken") != nil {
		t.Fatal("a project whose create failed is registered in the manager")
	}
	// On Windows this also proves the ledger handle was closed: RemoveAll only
	// succeeds when no SQLite connection still holds the directory.
	if _, statErr := os.Stat(filepath.Join(root, "p-broken")); !os.IsNotExist(statErr) {
		t.Fatalf("create left its directory behind: %v", statErr)
	}
	if len(m.ids) != 0 {
		t.Fatalf("manager keeps an id for the failed create: %v", m.ids)
	}
}

// create must never adopt a directory id that is already on disk. uniqueID's
// last-resort fallback does not re-check the disk, and MkdirAll used to merge
// into whatever was there: a reused id would open the other project's ledger
// and overwrite its manifest.
func TestCreateRefusesExistingDirectoryInsteadOfAdoptingIt(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	taken := filepath.Join(root, "taken")
	if err := os.MkdirAll(filepath.Join(taken, "cases"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(taken, ManifestFile)
	if err := os.WriteFile(manifest, []byte("{\"name\":\"someone else\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.createWith("taken", "mine", "scope", (*Project).write); err == nil {
		t.Fatal("create adopted an existing project directory")
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("the existing project's manifest is gone: %v", err)
	}
	if !strings.Contains(string(raw), "someone else") {
		t.Fatalf("the existing project's manifest was overwritten: %s", raw)
	}
}

// The lifecycle lock is per project: a settings save on one project must not
// serialize every delete/update in the instance.
func TestLifecycleLockIsPerProject(t *testing.T) {
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.Create("a", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create("b", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Store.Close(); _ = b.Store.Close() })

	releaseA := a.LockLifecycle()
	acquired := make(chan struct{})
	go func() {
		releaseB := b.LockLifecycle()
		releaseB()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		releaseA()
		t.Fatal("a second project waited for the first project's lifecycle lock")
	}
	releaseA()
}
