package api

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// "Review is off" has two meanings in this server: the operator's switch
// (ReviewsDisabled, what the dispatch gate is given) and ReviewsDisabled-or-no-
// verifier (reviewsOff, used where a console has to describe a state). The
// admission rule must follow the first one only, otherwise a project with no
// verifier configured lets the page call a premise usable while the gate refuses
// the direction for it.
//
// The fixture is exactly that shape: no Verifier on the server, the switch open,
// one unverified reviewable premise. Both surfaces have to refuse it, and the page
// must name the reason with the gate's own words — the mutation is passing
// s.reviewsOff() into premiseIssue instead of the flag, which flips the page to
// the "missing receipt" wording and empties blockedPremises.
func TestPremiseIssueFollowsTheDispatchSwitchNotTheMissingVerifier(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// No Verifier and no project: reviewsOff() is true here, ReviewsDisabled() is
	// false — the two notions this test exists to separate.
	s := &Server{CaseRoot: t.TempDir(), Store: st}
	if s.ReviewsDisabled() {
		t.Fatal("fixture: the review switch must start open")
	}
	f, err := st.AddFact(model.Fact{Kind: "vuln", Title: "越权", Status: "unverified", ClaimVersion: 2, ClaimHash: "h2"})
	if err != nil {
		t.Fatal(err)
	}
	in := model.Intent{ID: "in_flag", Title: "顺着越权打下去", FromFactIDs: []string{f.ID}}

	got := s.readiness(st.Snapshot(), in, s.premisePolicy(), false)
	if got.Ready || len(got.Premises) != 1 || got.Premises[0].Usable {
		t.Fatalf("a premise with no verifier configured was admitted: %+v", got)
	}
	want := model.PremiseIssue(f, model.PremiseVerifiedOnly)
	if got.Premises[0].Reason != want {
		t.Fatalf("page reason = %q, want the gate's own %q", got.Premises[0].Reason, want)
	}
	blocked := s.blockedPremises(in)
	if len(blocked) != 1 || !strings.Contains(blocked[0], want) {
		t.Fatalf("blockedPremises = %v, want one entry naming %q", blocked, want)
	}

	// The control: closing the switch is the state that does relax admission, and
	// both surfaces follow it. Without this the test above would also pass if the
	// relaxation had been dropped entirely.
	s.verifyDisabled.Store(true)
	if got := s.readiness(st.Snapshot(), in, s.premisePolicy(), false); !got.Ready {
		t.Fatalf("with the review switch closed the premise stayed blocked: %+v", got.Premises)
	}
	if blocked := s.blockedPremises(in); len(blocked) != 0 {
		t.Fatalf("blockedPremises = %v with the review switch closed, want none", blocked)
	}
}
