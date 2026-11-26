package api

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// A dispatch whose evidence chain is thin still runs and leaves a note. Reviews
// are off here so the verdict gate does not apply (that fork is covered in
// internal/store); the gap in this case is a registered original without a
// digest: it cannot be cited, and that must be visible in the journal and on the
// board glance instead of being a silent veto or a silent pass.
func TestDispatchRecordsEvidenceGapAsNote(t *testing.T) {
	s, p := newBindingServer(t)
	empty, err := p.Store.AddEvidence(model.Evidence{URI: "legacy/proof.html"})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := p.Store.AddFact(model.Fact{Kind: "vuln", Title: "越权", EvidenceIDs: []string{empty.ID}})
	if err != nil {
		t.Fatal(err)
	}
	in, err := p.Store.CreateIntent(model.Intent{Title: "顺着越权打下去", FromFactIDs: []string{fact.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := p.Store.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	_, gaps, err := p.Store.PrepareRunInput(run.ID, in, s.premisePolicy(), store.DispatchGate{ReviewsOff: true})
	if err != nil {
		t.Fatalf("dispatch refused a thin evidence chain instead of recording it: %v", err)
	}
	if len(gaps) == 0 {
		t.Fatal("no gap was produced for an original without a digest")
	}
	openTestJournal(t, s, run.ID)
	s.noteDispatchGaps(run.ID, in.ID, gaps)

	found := false
	for _, line := range readJournalFileLines(t, s.DataDir, run.ID) {
		if line["kind"] != "dispatch_gap" {
			continue
		}
		text, _ := line["text"].(string)
		if !strings.Contains(text, in.ID) || !strings.Contains(text, "保全摘要") {
			t.Fatalf("dispatch_gap line does not name the direction and the gap: %q", text)
		}
		found = true
	}
	if !found {
		t.Fatal("the journal has no dispatch_gap line")
	}
	if saved, _ := p.Store.GetRun(run.ID); saved.LastEventKind != "dispatch_gap" {
		t.Fatalf("board glance kind = %q, want dispatch_gap", saved.LastEventKind)
	}
}
