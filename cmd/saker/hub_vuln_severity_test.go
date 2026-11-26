package main

import (
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
	"github.com/kksty/saker/internal/store"
	"github.com/kksty/saker/internal/vulnlib"
)

// The library outlives the project, so the entry must carry the fact's grade at
// write time; after a delete there is no ledger left to copy it from.
func TestRecordVulnCarriesSeverityIntoTheLibrary(t *testing.T) {
	lib, err := vulnlib.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &project.Project{
		Manifest: project.Manifest{ID: "p_1", Name: "proj"},
		CaseDir:  t.TempDir(),
		Store:    st,
	}
	h := &hub{vulns: lib}
	h.recordVuln(p, model.Fact{
		ID: "f_1", Kind: "vuln", Title: "越权读订单", Status: "verified",
		Severity: model.SeverityCritical,
	}, nil)
	entries := lib.List("p_1", "", 10)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if got := entries[0].Severity; got != model.SeverityCritical {
		t.Fatalf("entry severity = %q, want %q", got, model.SeverityCritical)
	}
}
