package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/model"
)

func sqliteLedger(t *testing.T) *Store {
	t.Helper()
	s, e := OpenSQLite(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestUnavailableSQLiteDoesNotPublishWorkingRows(t *testing.T) {
	s := sqliteLedger(t)
	f, err := s.AddFact(model.Fact{Title: "committed"})
	if err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := s.AddFact(model.Fact{Title: "failed write"}); err == nil {
			t.Fatal("closed ledger reported success")
		}
		got := s.Snapshot()
		if len(got.Facts) != 1 || got.Facts[0].ID != f.ID || got.ReadRevision != before.ReadRevision ||
			got.ContentHash != before.ContentHash || len(got.Memory.History) != len(before.Memory.History) {
			t.Fatalf("uncommitted write changed the published ledger: %+v", got.Facts)
		}
	}
}

func TestSQLiteCommitsVersionsAndRestoresFailedTransaction(t *testing.T) {
	s := sqliteLedger(t)
	f, e := s.AddFact(model.Fact{Title: "old observation", Doc: "original evidence"})
	if e != nil {
		t.Fatal(e)
	}
	before := s.ContentHash()
	_, version, _ := s.FactVersion(f.ID)
	if _, e = s.sqlite.db.Exec(`CREATE TRIGGER fail_fact BEFORE UPDATE ON objects WHEN NEW.kind='fact' BEGIN SELECT RAISE(ABORT,'fixture disk transaction failure'); END`); e != nil {
		t.Fatal(e)
	}
	_, e = s.UpdateFact(f.ID, func(f *model.Fact) { f.Doc = "must roll back" })
	if e == nil {
		t.Fatal("failed commit accepted")
	}
	got, _ := s.FactByID(f.ID)
	if got.Doc != "original evidence" || s.ContentHash() != before || func() bool { _, v, _ := s.FactVersion(f.ID); return v != version }() {
		t.Fatal("failed transaction leaked")
	}
	if _, e = s.sqlite.db.Exec("DROP TRIGGER fail_fact"); e != nil {
		t.Fatal(e)
	}
	_, e = s.UpdateFact(f.ID, func(f *model.Fact) { f.Doc = "revised evidence" })
	if e != nil {
		t.Fatal(e)
	}
	history := s.History("fact", f.ID)
	if len(history) != 2 || s.ContentHash() == before {
		t.Fatalf("history=%d", len(history))
	}
	var old model.Fact
	if e = json.Unmarshal(history[0].Value, &old); e != nil || old.Doc != "original evidence" {
		t.Fatal("historical observation changed")
	}
	path := s.Path()
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s2, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s2.Close()
	if s2.ContentHash() != s.ContentHash() || len(s2.History("fact", f.ID)) != 2 {
		t.Fatal("restart lost versions")
	}
}

func TestConfidenceRepairPreservesLegacyVerificationAndReview(t *testing.T) {
	s := sqliteLedger(t)
	premise := observedPremise(t, s, "unverified premise")
	in := direction(t, s, "dependent", premise.ID)
	f, err := s.AddFact(model.Fact{IntentID: in.ID, Title: "legacy observation"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "r_test"}); err != nil {
		t.Fatal(err)
	}
	// Recreate the pre-derivation form of an observed claim and confirm the
	// old fallback binds it to the same assertion as declared observed.
	s.mu.Lock()
	for i := range s.db.Facts {
		row := &s.db.Facts[i]
		if row.ID == f.ID {
			row.Confidence, row.DeclaredConfidence = "observed", ""
		}
	}
	err = s.flush("fact")
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	plan := PlanConfidenceRepair(before)
	if len(plan.Changes) != 1 || len(plan.Blocked) != 0 || plan.Changes[0].FactID != f.ID || plan.Changes[0].After != "inferred" {
		t.Fatalf("legacy plan = %+v", plan)
	}
	if _, err := s.AddFact(model.Fact{Title: "another committed fact"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RepairConfidence(plan.Version); err == nil {
		t.Fatal("accepted a stale repair preview")
	}
	if err := s.RepairConfidence(PlanConfidenceRepair(s.Snapshot()).Version); err != nil {
		t.Fatal(err)
	}
	after := s.Snapshot()
	got, _ := s.FactByID(f.ID)
	if got.Status != "verified" || got.Confidence != "inferred" || got.DeclaredConfidence != "observed" || got.ClaimVersion != f.ClaimVersion || got.ClaimHash != f.ClaimHash {
		t.Fatalf("repair changed the reviewed claim: %+v", got)
	}
	if len(after.Reviews) != 1 || after.Reviews[0] != before.Reviews[0] || len(PlanConfidenceRepair(after).Changes) != 0 {
		t.Fatalf("repair changed receipt or failed to converge: %+v", after.Reviews)
	}
}

func TestBackupToIsAStandaloneReadOnlySnapshot(t *testing.T) {
	s := sqliteLedger(t)
	f, err := s.AddFact(model.Fact{Title: "snapshotted", Doc: "committed before backup"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddEvidence(model.Evidence{URI: "in_1/proof.txt", IntentID: "in_1"}); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "copy.sqlite")
	if err := s.BackupTo(backup); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	if _, err := os.Stat(backup + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("backup must not leave a WAL beside it: %v", err)
	}
	db, err := ReadSnapshot(backup)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if db.Version != CurrentSchemaVersion() || len(db.Facts) != 1 || db.Facts[0].ID != f.ID {
		t.Fatalf("snapshot does not carry the committed fact: %+v", db)
	}
	if len(db.Evidence) != 1 {
		t.Fatalf("snapshot evidence = %d, want 1", len(db.Evidence))
	}
	// The live store keeps working after the snapshot.
	if _, err := s.AddFact(model.Fact{Title: "after"}); err != nil {
		t.Fatal(err)
	}
}

func TestReadSnapshotRefusesFutureSchema(t *testing.T) {
	s := sqliteLedger(t)
	backup := filepath.Join(t.TempDir(), "future.sqlite")
	if err := s.BackupTo(backup); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, err := s.sqlite.db.Exec("UPDATE meta SET payload=json_set(payload,'$.version',999) WHERE id=1")
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BackupTo(backup); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(backup); err == nil {
		t.Fatal("a future schema must be refused")
	}
}

func TestIndexRebuildPreservesContentAndVersions(t *testing.T) {
	s := sqliteLedger(t)
	f, err := s.AddFact(model.Fact{Title: "original credential", Tags: []string{"original"}})
	if err != nil {
		t.Fatal(err)
	}
	before, watermark := s.ContentHash(), s.Watermark()
	f.Tags[0] = "external mutation"
	got, _ := s.FactByID(f.ID)
	if got.Tags[0] != "original" {
		t.Fatal("caller mutated committed fact")
	}
	if _, err = s.sqlite.db.Exec("DELETE FROM fact_search"); err != nil {
		t.Fatal(err)
	}
	if err = s.RebuildIndexes(); err != nil {
		t.Fatal(err)
	}
	matches, used, err := s.FullTextCandidates("credential")
	if err != nil || !used || len(matches) != 1 {
		t.Fatalf("index not restored: %v %v", matches, err)
	}
	if before != s.ContentHash() || watermark != s.Watermark() {
		t.Fatal("derived rebuild changed content")
	}
}
