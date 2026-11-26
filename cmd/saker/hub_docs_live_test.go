package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/decider"
	"github.com/kksty/saker/internal/store"
)

// Editing a project document used to reach only half the system: executors read
// the ledger live, while the commander round carried a copy taken when the
// runtime was attached. The operator's new 起点/约束 therefore applied to the
// next executor brief and not to the commander planning it (and the prompt
// budget was computed on the old text). The decider now reads the documents from
// the server at the start of every round, so this test drives real rounds across
// a save.
func TestDocumentSaveReachesTheNextCommanderRound(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{})
	p, err := mgr.Create("docs", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	k := h.attach(p)
	t.Cleanup(func() { k.cancel() })
	if k.dec == nil {
		t.Fatal("attach built no decider")
	}
	var prompts []string
	k.dec.Commander = func(_ context.Context, prompt string, _ bool) (decider.RoundResult, error) {
		prompts = append(prompts, prompt)
		return decider.RoundResult{}, nil
	}
	k.dec.ModelName = "fixture"
	// The hub refuses to dispatch when the local host is not configured; this
	// test is about the prompt's documents, not about dispatch, so it drops the
	// gate instead of building a fake host.
	k.dec.DispatchReady = nil
	round := func() {
		t.Helper()
		if k.dec.Run(context.Background(), "operator", true) == nil {
			t.Fatal("round did not run")
		}
	}

	round()
	// The operator edits the 起点 through the same store write the documents API
	// performs (no settings save, no restart).
	doc, err := p.Store.SaveDocument(0, store.DocumentScope, "只测 10.0.0.0/24", collab.OperatorID)
	if err != nil {
		t.Fatal(err)
	}
	// A stale revision is refused, and a save that changes nothing writes nothing:
	// neither may move what the commander reads.
	if _, err := p.Store.SaveDocument(0, store.DocumentScope, "冲突的旧编辑", collab.OperatorID); !errors.Is(err, store.ErrDocumentConflict) {
		t.Fatalf("stale save = %v, want ErrDocumentConflict", err)
	}
	same, err := p.Store.SaveDocument(doc.Revision, store.DocumentScope, "只测 10.0.0.0/24", collab.OperatorID)
	if err != nil {
		t.Fatalf("unchanged save: %v", err)
	}
	if same.Revision != doc.Revision {
		t.Fatalf("unchanged save moved the revision %d → %d", doc.Revision, same.Revision)
	}
	round()

	if len(prompts) != 2 {
		t.Fatalf("rounds = %d, want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], "只测 10.0.0.0/24") {
		t.Fatalf("the round after the save still plans on the old brief:\n%s", prompts[1])
	}
	for _, stale := range []string{"冲突的旧编辑"} {
		if strings.Contains(prompts[1], stale) {
			t.Fatalf("the round carries a rejected edit (%q): only a committed revision may reach the prompt", stale)
		}
	}
	// One source: the same read the executor brief and the project settings use.
	if got := k.dec.Docs(); !strings.Contains(got, "只测 10.0.0.0/24") {
		t.Fatalf("the decider's document source does not carry the saved text: %q", got)
	}
	if got := h.scopeOf(p); !strings.Contains(got, "只测 10.0.0.0/24") {
		t.Fatalf("the executor-side scope still reads the old text: %q", got)
	}
	if strings.Contains(prompts[0], "只测 10.0.0.0/24") {
		t.Fatal("control: the first round already carried the text saved after it, so the assertion above proves nothing")
	}
}
