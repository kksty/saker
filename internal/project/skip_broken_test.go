package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// One project directory that cannot be loaded must not stop the instance.
//
// The trigger was our own failed delete: a directory left with only runs/ and no
// ledger used to make Open return an error, and cmd/saker turned that into a fatal
// exit — the console and every other engagement never started. The load path now
// skips it and reports it, and it must do so without repairing anything: deleting
// the directory or writing a placeholder ledger into it would turn a recoverable
// load error into data loss.
func TestOpenSkipsAProjectWithoutLedgerButKeepsTheRest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	seed, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	good, err := seed.Create("正常项目", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := good.Store.Close(); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(root, "p_broken")
	if err := os.MkdirAll(filepath.Join(broken, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "runs", "r_1.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mgr, err := Open(root)
	if err != nil {
		t.Fatalf("一个坏项目被当成了实例启动失败：%v", err)
	}
	if got := mgr.Len(); got != 1 {
		t.Fatalf("loaded projects = %d, want 1 (the healthy one)", got)
	}
	skipped := mgr.Skipped()
	if len(skipped) != 1 || skipped[0].ID != "p_broken" {
		t.Fatalf("skipped = %+v, want exactly p_broken", skipped)
	}
	if skipped[0].Dir != broken || !filepath.IsAbs(skipped[0].Dir) {
		t.Fatalf("skipped dir = %q, want the absolute directory %q", skipped[0].Dir, broken)
	}
	if !strings.Contains(skipped[0].Err, "ledger") {
		t.Fatalf("skipped error lost the original reason: %q", skipped[0].Err)
	}
	if mgr.Get("p_broken") != nil {
		t.Fatal("a failed load must not be served as a project")
	}
	if _, ok := mgr.SkippedProject("p_broken"); !ok {
		t.Fatal("SkippedProject does not report the directory it just skipped")
	}

	// Nothing was written into the broken directory: no placeholder ledger, no
	// case structure. Either would make the next start look like an empty but
	// healthy engagement.
	for _, name := range []string{"ledger.sqlite", "cases"} {
		if _, err := os.Stat(filepath.Join(broken, name)); !os.IsNotExist(err) {
			t.Fatalf("load wrote %s into the broken directory (err=%v)", name, err)
		}
	}

	// The healthy project is a real, writable project, not just a row in a list.
	loaded := mgr.Get(good.ID)
	if loaded == nil {
		t.Fatalf("healthy project %s is missing after the skipped directory", good.ID)
	}
	in, err := loaded.Store.CreateIntent(model.Intent{Title: "仍然可用"})
	if err != nil {
		t.Fatalf("healthy project is not writable: %v", err)
	}
	if _, ok := loaded.Store.GetIntent(in.ID); !ok {
		t.Fatal("healthy project does not read back what it just wrote")
	}
}

// A directory whose ledger exists but cannot be opened is the same class: it is
// reported with the original error and the instance keeps going.
func TestOpenSkipsAnUnreadableLedger(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	bad := filepath.Join(root, "p_badledger")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	// An empty file is the ledger open's explicit refusal (not a fresh ledger).
	if err := os.WriteFile(filepath.Join(bad, "ledger.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mgr, err := Open(root)
	if err != nil {
		t.Fatalf("unreadable ledger stopped the instance: %v", err)
	}
	if mgr.Len() != 0 {
		t.Fatalf("loaded = %d, want 0", mgr.Len())
	}
	skipped := mgr.Skipped()
	if len(skipped) != 1 || skipped[0].ID != "p_badledger" {
		t.Fatalf("skipped = %+v", skipped)
	}
	if !strings.Contains(skipped[0].Err, "empty SQLite ledger") {
		t.Fatalf("skipped error = %q, want the ledger open's own reason", skipped[0].Err)
	}
	if info, err := os.Stat(filepath.Join(bad, "ledger.sqlite")); err != nil || info.Size() != 0 {
		t.Fatalf("the refused ledger was touched: info=%v err=%v", info, err)
	}
}
