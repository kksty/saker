package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
	"github.com/kksty/saker/internal/store"
)

// ErrPremiseUnverified is the verified_only refusal: a premise cannot be used
// as an actionable predecessor yet (rejected, not a reviewable kind, or lacking
// current-version proof). It is a 400 with code=premise_unverified, not a 409:
// the request is well-formed, the ledger simply does not accept that assertion
// as an actionable predecessor yet.
var ErrPremiseUnverified = errors.New("前提必须在当前断言版本通过独立核验")


// premisePolicy is the effective admission mode of the current project. There is
// one mode; bare test servers use it too.
func (s *Server) premisePolicy() string {
	if s.Project == nil {
		return project.PremisePolicyVerifiedOnly
	}
	return s.Project.PremisePolicyView()
}

// premiseIssue is the admission rule as this project applies it right now.
// With review disabled no verdict can ever arrive, so requiring one would make
// every direction that names a premise permanently unusable; withdrawn and
// rejected facts, and kinds a review can never establish, still refuse. A
// temporary pause is not this state.
//
// reviewsOff is a parameter rather than a lookup on purpose: the caller must pass
// the same flag the dispatch gate is given (s.ReviewsDisabled()), because two
// notions of "review is off" — this one and s.reviewsOff(), which also counts a
// missing verifier — made the page and the gate answer differently for a project
// with no verifier configured.
func (s *Server) premiseIssue(f model.Fact, reviewsOff bool) string {
	issue := model.PremiseIssue(f, s.premisePolicy())
	if issue == "" || !reviewsOff {
		return issue
	}
	if f.Status == "withdrawn" || f.Status == "rejected" || !model.ReviewableKind(f.Kind) {
		return issue
	}
	return ""
}

// checkPremiseRefs validates the from_fact_ids a caller is about to bind to a
// new direction. Existence is mandatory; rejected and non-reviewable premises
// are rejected immediately, while a reviewable-but-pending relation may be
// registered so the fixed review phase can prove it and dispatch stays gated
// until it is proven. The error preserves which rule failed so the HTTP mapping
// can give the caller a machine code.
func (s *Server) checkPremiseRefs(ids []string) error {
	policy := s.premisePolicy()
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		f, ok := s.Store.FactByID(id)
		if !ok {
			return fmt.Errorf("%w：%s", store.ErrFactRefMissing, id)
		}
		if policy != model.PremiseVerifiedOnly {
			continue
		}
		if f.Unusable() || !model.ReviewableKind(f.Kind) {
			// A non-reviewable kind (note) is not an unverified assertion: no
			// verdict can ever make it a premise, in any review mode. Say what to
			// do instead, so a commander does not conclude that its evidence is
			// unusable and drop from_fact_ids altogether.
			if !f.Unusable() && !model.ReviewableKind(f.Kind) {
				return fmt.Errorf("%w：%s（%s；%s 不是可复核的断言，不能作为必要前提——用 context_fact_ids 引用它，或把结论写进描述）",
					ErrPremiseUnverified, id, model.PremiseIssue(f, policy), f.Kind)
			}
			return fmt.Errorf("%w：%s（%s）", ErrPremiseUnverified, id, model.PremiseIssue(f, policy))
		}
		// Persist the intended relation while its proof is pending: a reviewable
		// fact with evidence is admitted to the queue, and dispatch stays gated
		// until the verdict arrives.
	}
	return nil
}

// writePremiseError maps the premise admission errors to 400 plus a machine
// code. The message names the fact and the failing rule, so a commander or
// operator can act without guessing.
func writePremiseError(w http.ResponseWriter, err error) {
	code := "premise_invalid"
	switch {
	case errors.Is(err, store.ErrFactRefMissing):
		code = "fact_ref_missing"
	case errors.Is(err, ErrPremiseUnverified):
		code = "premise_unverified"
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "code": code})
}

// premisePolicyResponse is the operator's view of the project admission mode.
// It is read-only on purpose: there is exactly one mode, the manifest is
// normalized to it on load, so a write endpoint could only restate the current
// value while looking like a decision. Sessions read the same value through
// readiness; they never see this route.
type premisePolicyResponse struct {
	PremisePolicy string `json:"premise_policy"`
	// Effective is what admission actually does: the policy while review runs,
	// or review_disabled once the switch is off — the configured mode alone
	// would describe a gate that no longer waits for a verdict.
	Effective string `json:"effective"`
	// ReviewDisabled is the runtime switch behind that difference.
	ReviewDisabled bool `json:"review_disabled"`
}

func (s *Server) getPremisePolicy(w http.ResponseWriter, r *http.Request) {
	pol := s.premisePolicy()
	eff := pol
	if s.ReviewsDisabled() {
		eff = "review_disabled"
	}
	writeJSON(w, http.StatusOK, premisePolicyResponse{PremisePolicy: pol, Effective: eff, ReviewDisabled: s.ReviewsDisabled()})
}
