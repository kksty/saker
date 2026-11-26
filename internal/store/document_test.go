package store

import (
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A document save is a new revision of one row, and the revision history is the
// ledger's own version journal: after a restart the current revision and every
// older one must still be readable. This is the whole audit story for the
// documents, so it is pinned here rather than in an API test.
func TestDocumentRevisionsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	st, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.SaveDocument(0, DocumentScope, "10.0.0.0/24", "operator")
	if err != nil || first.Revision != 1 {
		t.Fatalf("first save = %+v, %v", first, err)
	}
	second, err := st.SaveDocument(1, DocumentScope, "10.0.0.0/24, 192.168.1.0/24", "operator")
	if err != nil || second.Revision != 2 {
		t.Fatalf("second save = %+v, %v", second, err)
	}
	if _, err := st.SaveDocument(1, DocumentScope, "stale", "operator"); err != ErrDocumentConflict {
		t.Fatalf("stale revision = %v, want ErrDocumentConflict", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	cur, ok := reopened.Document(DocumentScope)
	if !ok || cur.Revision != 2 || cur.Text != "10.0.0.0/24, 192.168.1.0/24" {
		t.Fatalf("current document after reopen = %+v (ok=%v)", cur, ok)
	}
	old, ok := reopened.DocumentRevision(DocumentScope, 1)
	if !ok || old.Text != "10.0.0.0/24" {
		t.Fatalf("revision 1 after reopen = %+v (ok=%v)", old, ok)
	}
	if n := len(reopened.History("document", DocumentScope)); n != 2 {
		t.Fatalf("history rows after reopen = %d, want 2", n)
	}
}

// The goal document edits the goal itself in the same transaction as the
// revision bump, so the two cannot disagree.
func TestGoalDocumentEditsTheGoalAndBumpsRevision(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.AddGoal(model.Goal{Statement: "拿到管理员权限"})
	if err != nil {
		t.Fatal(err)
	}
	rev := st.GoalRevision(g.ID)
	doc, err := st.SaveGoalDocument(rev, g.ID, "拿到数据库读写权限", "", "", "", "operator")
	if err != nil || doc.Revision != rev+1 || doc.GoalID != g.ID {
		t.Fatalf("goal document = %+v, %v", doc, err)
	}
	if got := st.GoalRevision(g.ID); got != rev+1 {
		t.Fatalf("goal revision after edit = %d, want %d", got, rev+1)
	}
	for _, goal := range st.Goals() {
		if goal.ID == g.ID && goal.Statement != "拿到数据库读写权限" {
			t.Fatalf("goal statement = %q, want the document text", goal.Statement)
		}
	}
	// A stale revision is refused and changes nothing.
	if _, err := st.SaveGoalDocument(rev, g.ID, "别的目标", "", "", "", "operator"); err != ErrDocumentConflict {
		t.Fatalf("stale goal save = %v, want ErrDocumentConflict", err)
	}
}
