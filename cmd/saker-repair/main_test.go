package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
