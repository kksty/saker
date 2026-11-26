package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// A note is a memo, not an assertion: no verdict can ever make it a premise, in
// either review mode. The refusal must say where it does belong, so a commander
// does not conclude that its whole evidence set is unusable.
func TestNotePremiseRefusalNamesTheAlternative(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &blockingExec{}, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0
	note, err := st.AddFact(model.Fact{
		IntentID: "", Kind: "note", Title: "WFS-T 匿名可达写被层 ACL 全量拦截",
		Summary: "被拦", Confidence: "observed",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, reviewsOff := range []bool{false, true} {
		s.SetReviewsDisabled(reviewsOff)
		err := s.checkPremiseRefs([]string{note.ID})
		if err == nil {
			t.Fatalf("reviewsOff=%v: a note was accepted as a hard premise", reviewsOff)
		}
		if !strings.Contains(err.Error(), "context_fact_ids") {
			t.Fatalf("reviewsOff=%v: refusal does not point at context_fact_ids: %v", reviewsOff, err)
		}
	}
	// An unverified but reviewable premise is admitted in both modes: it queues
	// for review, and dispatch waits for the verdict (reviews off: no verdict is
	// coming, so readiness admits it).
	vuln, err := st.AddFact(model.Fact{Kind: "vuln", Title: "可疑写入", Summary: "s", Confidence: "observed"})
	if err != nil {
		t.Fatal(err)
	}
	for _, reviewsOff := range []bool{false, true} {
		s.SetReviewsDisabled(reviewsOff)
		if err := s.checkPremiseRefs([]string{vuln.ID}); err != nil {
			t.Fatalf("reviewsOff=%v: unverified reviewable premise refused: %v", reviewsOff, err)
		}
	}
	// A withdrawn premise stays refused in both modes.
	if _, err := st.UpdateFact(vuln.ID, func(f *model.Fact) { f.Status = "withdrawn" }); err != nil {
		t.Fatal(err)
	}
	s.SetReviewsDisabled(true)
	if err := s.checkPremiseRefs([]string{vuln.ID}); err == nil {
		t.Fatal("a withdrawn premise was accepted with reviews off")
	}
	_ = http.StatusOK
}
