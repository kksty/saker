package ledger

import (
	"github.com/kksty/saker/internal/model"
	"path/filepath"
	"testing"
	"time"
)

func TestPlaybookAliasesAndInvalidEntryDoNotPoisonBatch(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "in_1")
	st, run := newWarningLedger(t)
	writeFile(t, filepath.Join(work, HandoffDir, HandoffFile), `{"declaration_id":"decl_observation","kind":"note","title":"fixture"}`+"\n"+
		`{"kind":"playbook","title":"resolved","from_fact_ids":["d_observation"],"doc":"fixture"}`+"\n"+
		`{"kind":"playbook","title":"invalid","from_fact_ids":["missing"],"doc":"fixture"}`+"\n")
	(&Ingestor{Store: st}).OnFinish(run.ID, "in_1", root, work, time.Now().Add(time.Hour))
	f, _ := st.FindDeclaration("in_1", "", "decl_observation")
	books := st.Playbooks()
	if Consumed(work) == 0 || len(books) != 1 || len(books[0].FromFactIDs) != 1 || books[0].FromFactIDs[0] != f.ID {
		t.Fatalf("bad settlement: %v", books)
	}
	if _, err := st.AddFact(model.Fact{IntentID: "in_1", DeclarationID: "d_observation", Kind: "note", Title: "ambiguous"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ResolveFactReference(st, "in_1", "decl-observation"); ok {
		t.Fatal("ambiguous alias accepted")
	}
}
