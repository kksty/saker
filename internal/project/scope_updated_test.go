package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/saker/internal/store"
)

// The brief's revision time is what lets a reader tell "the operator changed the
// boundary after this authorization" from "this authorization is newer than the
// boundary". It must move only on a real text change, and it must survive a
// restart — an in-memory-only stamp would silently invert every precedence
// decision after a redeploy.
func TestBriefRevisionMovesOnlyOnRealEditsAndPersists(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("stamp", "初始边界")
	if err != nil {
		t.Fatal(err)
	}
	created := p.BriefRevision()
	if created.IsZero() || !created.Equal(p.Created) {
		t.Fatalf("fresh project revises at %v, created %v (want the creation time)", created, p.Created)
	}
	// Writing the same text is not an edit: a settings card that submits the
	// whole form must not keep pushing the boundary into the future.
	if err := m.SetScope(p.ID, "初始边界"); err != nil {
		t.Fatal(err)
	}
	if got := p.BriefRevision(); !got.Equal(created) {
		t.Fatalf("identical scope moved the stamp: %v -> %v", created, got)
	}
	if err := m.SetScope(p.ID, "放宽：订阅可测"); err != nil {
		t.Fatal(err)
	}
	afterScope := p.BriefRevision()
	if !afterScope.After(created) {
		t.Fatalf("scope edit did not advance the stamp: %v", afterScope)
	}
	if err := m.SetDescription(p.ID, "补充说明"); err != nil {
		t.Fatal(err)
	}
	afterDesc := p.BriefRevision()
	if !afterDesc.After(afterScope) && !afterDesc.Equal(afterScope) {
		t.Fatalf("description edit went backwards: %v -> %v", afterScope, afterDesc)
	}
	// Reopen: the stamp is manifest state, not process state.
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Get(p.ID).BriefRevision()
	if !got.Equal(afterDesc) {
		t.Fatalf("reopened stamp = %v, want %v", got, afterDesc)
	}
}

// A manifest written before this field existed has neither stamp nor, in the
// oldest case, a creation time. Zero is reported as zero — the reader must say
// "revision time unknown" instead of comparing it as if the brief were ancient.
// A project written before this field existed must not read as "the brief was
// just revised": load() dates such a manifest by its ledger's mtime, so an
// authorization issued after that mtime stays newer than the brief instead of
// being silently outranked.
func TestLegacyManifestIsDatedByItsLedgerNotByStartup(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := m.Create("legacy", "旧边界")
	if err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(created.Dir, "ledger.sqlite")
	longAgo := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(ledger, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(created.Dir, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "created")
	delete(fields, "scope_updated_at")
	trimmed, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created.Dir, "project.json"), trimmed, 0o644); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Get(created.ID)
	if got == nil {
		t.Fatal("legacy project not opened")
	}
	stamp := got.BriefRevision()
	if stamp.After(time.Now().Add(-time.Hour)) {
		t.Fatalf("legacy brief reads as freshly revised: %v", stamp)
	}
	// The first real edit from the operator establishes the current stamp, so
	// the state cannot stay dated to the old ledger forever.
	if err := reopened.SetScope(created.ID, "新边界"); err != nil {
		t.Fatal(err)
	}
	if stamp := got.BriefRevision(); stamp.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("first edit left the stamp at %v", stamp)
	}
}

// Once a document is edited, its ledger revision survives a manifest that still has the initial text.
func TestBriefUsesLedgerDocumentsAfterReopen(t *testing.T) {
	root := t.TempDir()
	mgr, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("Document project", "Original scope")
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetDescription(p.ID, "Original constraints"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.SaveDocument(0, store.DocumentScope, "Revised scope", "operator"); err != nil {
		t.Fatal(err)
	}
	doc, err := p.Store.SaveDocument(0, store.DocumentConstraints, "Revised constraints", "operator")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	project := reopened.Get(p.ID)
	if project == nil {
		t.Fatal("project did not reopen")
	}
	defer project.Store.Close()
	if project.ScopeText() != "Revised scope" || project.DescriptionText() != "Revised constraints" {
		t.Fatalf("wrong brief after reopen: %q / %q", project.ScopeText(), project.DescriptionText())
	}
	if !project.BriefRevisedAt().Equal(doc.UpdatedAt) {
		t.Fatalf("brief revision = %s, want %s", project.BriefRevisedAt(), doc.UpdatedAt)
	}
}
