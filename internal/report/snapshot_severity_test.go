package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// findings.json is written by the report run, not by Go; the deterministic half
// of that hop is the snapshot it must copy from. This locks the fact's grade
// into the snapshot so a report can never be told a different grade than the
// ledger holds.
func TestSnapshotCarriesTheFactSeverityForTheWriterToCopy(t *testing.T) {
	dir := t.TempDir()
	db := store.DB{Facts: []model.Fact{{
		ID: "f_1", Kind: "vuln", Title: "越权读订单", Status: "verified",
		Severity: model.SeverityCritical,
	}}}
	if err := writeSnapshot(dir, db, nil, "", nil, false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, Snapshot))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Facts []struct {
			ID       string `json:"id"`
			Severity string `json:"severity"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Facts) != 1 || s.Facts[0].ID != "f_1" {
		t.Fatalf("snapshot facts = %+v", s.Facts)
	}
	if got := s.Facts[0].Severity; got != model.SeverityCritical {
		t.Fatalf("snapshot severity = %q, want %q", got, model.SeverityCritical)
	}
}
