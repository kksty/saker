package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/store"
)

func TestLedgerNoteSurvivesProjectionFailureAndRestart(t *testing.T) {
	dir := t.TempDir()
	ledgerPath, path := filepath.Join(dir, "ledger.sqlite"), filepath.Join(dir, "state.md")
	st, err := store.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("legacy judgement"), 0600); err != nil {
		t.Fatal(err)
	}
	f := NewLedger(path, st)
	n, err := f.Snapshot()
	if err != nil || n.Version != 0 || n.State != "legacy judgement" {
		t.Fatalf("legacy note: %+v %v", n, err)
	}
	if _, ok := st.Notebook(); ok {
		t.Fatal("read implicitly migrated note")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	n, warning, err := f.Replace(0, "preserved judgement", "operator", "", nil)
	if err != nil || warning == "" || n.Version != 1 {
		t.Fatalf("projection failure hid committed result: %+v %q %v", n, warning, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := NewLedger(path, st).Load()
	if err != nil || got != "preserved judgement" {
		t.Fatalf("recovery depended on mirror: %q %v", got, err)
	}
}
