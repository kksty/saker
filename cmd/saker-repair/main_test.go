package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/ledger"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The repair tool edits a project while the control plane is stopped, so its
// argument checks are the first guard: a typo must not run against a relative
// path or a broad intent selector.
func TestRunRejectsUnsafeArguments(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no flags", nil},
		{"relative project", []string{"--project", "data/projects/p1", "--intent", "in_1"}},
		{"missing intent", []string{"--project", dir, "--intent", ""}},
		{"intent path", []string{"--project", dir, "--intent", "in_1/raw"}},
		{"dot intent", []string{"--project", dir, "--intent", "."}},
		{"dotdot intent", []string{"--project", dir, "--intent", ".."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(tc.args, &out)
			if err == nil || !strings.Contains(err.Error(), "必须提供 --project 和精确的 --intent") {
				t.Fatalf("run(%v) = %v, want the argument error", tc.args, err)
			}
			if out.Len() != 0 {
				t.Fatalf("rejected run printed a plan: %q", out.String())
			}
		})
	}
}

func TestRunRequiresLedgerSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, map[string]any{"id": "p1", "name": "fixture", "paused": true})
	var out bytes.Buffer
	if err := run([]string{"--project", dir, "--intent", "in_1"}, &out); err == nil {
		t.Fatal("a paused project without its ledger must not plan a repair")
	}
}

func TestRunRefusesProjectThatIsNotPaused(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, map[string]any{"id": "p1", "name": "fixture", "paused": false})
	var out bytes.Buffer
	err := run([]string{"--project", dir, "--intent", "in_1"}, &out)
	if err == nil || !strings.Contains(err.Error(), "项目必须暂停") {
		t.Fatalf("run = %v, want the paused-project refusal", err)
	}
}

func TestRunPreviewsPlanAndRefusesEmptyCommit(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, map[string]any{"id": "p1", "name": "fixture", "paused": true})
	intentID := seedLedger(t, filepath.Join(dir, "ledger.sqlite"))
	// A handoff file that was already fully consumed: the plan has nothing to
	// advance, so the dry run prints it and --commit refuses to write.
	work := filepath.Join(dir, "cases", intentID)
	if err := os.MkdirAll(filepath.Join(work, ledger.HandoffDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ledger.HandoffDir, ledger.HandoffFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := run([]string{"--project", dir, "--intent", intentID}, &out); err != nil {
		t.Fatalf("preview: %v", err)
	}
	var plan ledger.CursorRepair
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("preview is not a plan: %v %s", err, out.String())
	}
	if plan.Before != 0 || plan.Through != 0 || len(plan.Problems) != 0 {
		t.Fatalf("unexpected empty plan: %+v", plan)
	}

	if err := run([]string{"--project", dir, "--intent", intentID, "--commit"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "没有待修复游标") {
		t.Fatalf("commit = %v, want the empty-cursor refusal", err)
	}
}

func TestRunRefusesUnknownIntent(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, map[string]any{"id": "p1", "name": "fixture", "paused": true})
	seedLedger(t, filepath.Join(dir, "ledger.sqlite"))
	err := run([]string{"--project", dir, "--intent", "in_missing"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "项目中没有意图") {
		t.Fatalf("run = %v, want the unknown-intent refusal", err)
	}
}

func TestConfidenceRepairPreviewsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, map[string]any{"id": "p1", "name": "fixture", "paused": true})
	seedLedger(t, filepath.Join(dir, "ledger.sqlite"))
	var out bytes.Buffer
	if err := run([]string{"--project", dir, "--confidence"}, &out); err != nil {
		t.Fatal(err)
	}
	var plan store.ConfidenceRepair
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan.Version.Epoch == "" || len(plan.Changes) != 0 || len(plan.Blocked) != 0 {
		t.Fatalf("confidence preview = %s (%v)", out.String(), err)
	}
	if backups, err := filepath.Glob(filepath.Join(dir, "ledger-before-confidence-*.sqlite")); err != nil || len(backups) != 0 {
		t.Fatalf("preview wrote a backup: %v (%v)", backups, err)
	}
}

func TestCursorPreviewLocatesFailedFactAfterPartialSettlement(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, map[string]any{"id": "p1", "name": "fixture", "paused": true})
	path := filepath.Join(dir, "ledger.sqlite")
	intentID := seedLedger(t, path)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	caseRoot := filepath.Join(dir, "cases")
	work := filepath.Join(caseRoot, intentID)
	handoff := filepath.Join(work, ledger.HandoffDir)
	if err := os.MkdirAll(handoff, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "proof.txt"), []byte("fixture evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := `{"declaration_id":"kept","kind":"note","title":"observation with evidence","evidence":["proof.txt"]}` + "\n"
	second := `{"declaration_id":"missing","kind":"note","title":"uncommitted observation"}` + "\n"
	if err := os.WriteFile(filepath.Join(handoff, ledger.HandoffFile), []byte(first+second), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Interrupt settlement after one fact and its original are persisted. The
	// preview must isolate the missing declaration without consuming either line.
	_, err = db.Exec(`CREATE TRIGGER fail_fixture_fact BEFORE INSERT ON objects
		WHEN NEW.kind = 'fact' AND instr(CAST(NEW.payload AS TEXT), '"declaration_id":"missing"') > 0
		BEGIN SELECT RAISE(ABORT,'fixture write failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	(&ledger.Ingestor{Store: st}).OnFinish("fixture-run", intentID, caseRoot, work, time.Now().Add(time.Hour))
	before, err := store.ReadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := run([]string{"--project", dir, "--intent", intentID}, &out); err != nil {
		t.Fatal(err)
	}
	var plan ledger.CursorRepair
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Persisted != 1 || plan.Matched != 1 || plan.Before != 0 || plan.Through != int64(len(first)+len(second)) || len(plan.Pending) != 1 {
		t.Fatalf("incorrect partial batch: %+v", plan)
	}
	pending := plan.Pending[0]
	wantLocation := fmt.Sprintf(".saker/facts.jsonl#bytes=%d-%d", len(first), len(first)+len(second))
	if pending.DeclarationID != "missing" || pending.Location != wantLocation || len(pending.Reasons) != 1 || !strings.Contains(pending.Reasons[0], "匹配 0") {
		t.Fatalf("incorrect pending declaration: %+v", pending)
	}
	if _, err := os.Stat(filepath.Join(handoff, ledger.HandoffOffset)); !os.IsNotExist(err) {
		t.Fatalf("preview wrote a cursor: %v", err)
	}
	after, err := store.ReadSnapshot(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("preview changed the ledger: %v", err)
	}
}

func writeManifest(t *testing.T, dir string, content map[string]any) {
	t.Helper()
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedLedger writes a real ledger with one pending direction and returns the
// generated intent id (the store owns the short-id scheme).
func seedLedger(t *testing.T, path string) string {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{Title: "fixture"})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return in.ID
}
