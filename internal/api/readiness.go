package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/kksty/tianwu/internal/evidence"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// Derived review lifecycle shown with a blocked premise under verified_only.
// It is deliberately not the review queue or a verifier run: it answers only
// whether this fact is waiting for, inside, or unable to receive an independent
// proof, so the commander can wait or ask the operator instead of probing the
// operator-only review surface (which stays 404 for a session).
const (
	reviewStateQueued     = "queued"
	reviewStateRunning    = "running"
	reviewStateCooling    = "cooling"
	reviewStatePaused     = "paused"
	reviewStateFailed     = "failed"
	reviewStateIneligible = "ineligible"
	reviewStateDisabled   = "disabled"
)

type premiseReview struct {
	State string `json:"state"`
}

type premiseReadiness struct {
	ID           string `json:"id"`
	Title        string `json:"title,omitempty"`
	ClaimVersion uint64 `json:"claim_version,omitempty"`
	Status       string `json:"status"`
	Usable       bool   `json:"usable"`
	Reason       string `json:"reason,omitempty"`
	ReviewID     string `json:"review_id,omitempty"`
	VerifierRun  string `json:"verifier_run,omitempty"`
	Integrity    string `json:"integrity"`
	// Review is present only when verified_only blocks this premise and review
	// could still change its usability. Standard policy omits it: there review is
	// not an admission gate, and a queued state would imply one.
	Review *premiseReview `json:"review,omitempty"`
}

type intentReadiness struct {
	IntegrityChecked bool               `json:"integrity_checked"`
	IntentID         string             `json:"intent_id"`
	Policy           string             `json:"policy"`
	Ready            bool               `json:"ready"`
	Premises         []premiseReadiness `json:"premises"`
	// ReviewHint is one operator-readable sentence for the blocking review
	// states above. The session cannot trigger a review; the hint says so and
	// tells it whether to wait or ask the operator.
	ReviewHint string `json:"review_hint,omitempty"`
	// SelfReferenceHint warns about a structural circularity: an unverified
	// premise names the same entity this direction says it is interested in. A
	// re-probe/verification direction then waits for a review that performs the
	// same probe, and would repeat it when the review passes. It is advisory:
	// the same shape is legitimate for a deeper direction that merely operates
	// on that host, so the control plane must not block it.
	SelfReferenceHint string `json:"self_reference_hint,omitempty"`
}


type readinessInputs struct {
	facts   map[string]model.Fact
	proofs  map[string]model.Evidence
	reviews []model.Review
}

func newReadinessInputs(db store.DB) readinessInputs {
	inputs := readinessInputs{
		facts:   make(map[string]model.Fact, len(db.Facts)),
		proofs:  make(map[string]model.Evidence, len(db.Evidence)),
		reviews: db.Reviews,
	}
	for _, f := range db.Facts {
		inputs.facts[f.ID] = f
	}
	for _, e := range db.Evidence {
		inputs.proofs[e.ID] = e
	}
	return inputs
}

// Readiness explains the same rule dispatch records. Integrity is a separate
// dimension: valid bytes cannot verify an assertion's meaning, and a row that
// cannot be re-proven is a recorded gap, not a veto.
//
// checkBytes is a required parameter, not a default: the board and the park
// click cannot afford to look up every original, while the session's premise
// projection must answer with the same predicate the freeze records. A variadic
// default made that answer an accident of the signature — one changed default
// would have silently widened it.
func (s *Server) readiness(db store.DB, in model.Intent, policy string, checkBytes bool) intentReadiness {
	return s.readinessFromInputs(newReadinessInputs(db), in, policy, checkBytes)
}

func (s *Server) readinessFromInputs(inputs readinessInputs, in model.Intent, policy string, checkBytes bool) intentReadiness {
	out := intentReadiness{IntentID: in.ID, Policy: policy, Ready: true, Premises: []premiseReadiness{}}
	checkedIntegrity := false
	var reviewStates []string
	// Review disabled at runtime (--no-verify at boot, or the operator's switch):
	// no verdict can ever arrive, so requiring one would block every direction that
	// names a premise forever. A temporary pause is NOT this state: a pause is
	// reversible, and relaxing admission under it would freeze a direction whose
	// review was only deferred.
	reviewsOff := s.ReviewsDisabled()
	for _, id := range in.FromFactIDs {
		row := premiseReadiness{ID: id, Status: "missing", Integrity: "unknown", Reason: "前提记录不存在"}
		f, ok := inputs.facts[id]
		if ok {
			row.Title, row.Status, row.ClaimVersion = f.Title, f.Status, f.ClaimVersion
			row.Reason = s.premiseIssue(f, reviewsOff)
			// Only the verdict gate is dropped: a note or a withdrawn row is not
			// waiting for a review.
			if reviewsOff && model.PremiseIssueReviewOnly(f) {
				row.Reason = ""
			}
			if proof, proven := model.CurrentProof(f, inputs.reviews); proven {
				row.ReviewID, row.VerifierRun = proof.ID, proof.VerifierRun
			} else if !reviewsOff && policy == model.PremiseVerifiedOnly && row.Reason == "" {
				row.Reason = "缺少绑定当前断言的独立核验收据"
			}
			// The dispatch gate refuses a premise whose producer has not settled;
			// readiness must say the same thing or the page shows "usable" for a
			// direction that cannot start. One rule, two readers.
			if row.Reason == "" && s.Store != nil {
				if owner := s.Store.PremiseOwnerStillLive(f, in.ID); owner != "" {
					row.Reason = fmt.Sprintf("来源 %s 还没收口：现在不能当必要前提；等它收口，或改用 context_fact_ids 只读参考", owner)
				}
			}
			// Two different things wear the word "missing evidence": nothing was
			// ever registered (a candidate relation the model built on its own —
			// advisory), or an original was registered and cannot be opened. The
			// second is record-keeping too: the page shows Integrity=missing and
			// the dispatch gate appends dispatch_gap, but neither refuses the run.
			if policy == model.PremiseVerifiedOnly {
				checkedIntegrity = true
				row.Integrity = "none"
				if len(f.EvidenceIDs) > 0 {
					row.Integrity = "not_checked"
					if checkBytes {
						row.Integrity = "intact"
					}
					for _, eid := range f.EvidenceIDs {
						e, exists := inputs.proofs[eid]
						// A registered row without a digest, or one the ledger no
						// longer has, is a gap in the record — not a verdict. The
						// dispatch gate records it as dispatch_gap and still
						// dispatches (store.PrepareRunInput), so readiness must not
						// flip Usable/Ready here; Integrity=missing is the hint the
						// page shows next to the premise.
						if !exists || !store.EvidenceVerifiable(e) {
							row.Integrity = "missing"
							break
						}
						if !checkBytes {
							continue
						}
						if err := evidence.Verify(s.CaseRoot, e); err != nil {
							row.Reason = fmt.Sprintf("证据 %s：%v", eid, err)
							row.Integrity = "invalid"
							break
						}
					}
				}
			}
		}
		row.Usable = row.Reason == ""
		out.Ready = out.Ready && row.Usable
		switch {
		case !reviewsOff && ok && row.Status == "unverified" && model.ReviewableKind(f.Kind):
			// Review can still change this observation, so the session learns
			// whether to wait.
			row.Review = s.premiseReview(f)
			reviewStates = append(reviewStates, row.Review.State)
		case !reviewsOff && !row.Usable:
			// Missing, rejected or unreviewable: waiting for a review cannot
			// make this premise usable.
			row.Review = &premiseReview{State: reviewStateIneligible}
			reviewStates = append(reviewStates, row.Review.State)
		}
		// A verification/re-probe direction often names the same entity in
		// interest_entity_refs as the unverified premise it waits on. This is a
		// hint, not a block.
		if ok && row.Status == "unverified" && f.EntityRef != "" && slices.Contains(in.InterestEntityRefs, f.EntityRef) {
			advice := "建议改成无前提 bootstrap，或等复核结果直接收口，不要重复探测。"
			if reviewsOff {
				advice = "复核已关闭：建议改成无前提 bootstrap，或直接引用这条事实推进，不要重复探测。"
			}
			out.SelfReferenceHint = fmt.Sprintf(
				"前提「%s」（%s）与本意图关注同一实体，且断言未核验；若本意图就是复探/验证该实体，复核与本意图会重复。%s",
				row.Title, f.EntityRef, advice)
		}
		out.Premises = append(out.Premises, row)
	}
	if len(reviewStates) > 0 {
		out.ReviewHint = reviewHint(reviewStates)
	}
	out.IntegrityChecked = checkedIntegrity
	return out
}
// reviewHint summarizes the blocking review states for a session. It stays a
// derived classification (not the queue or a verifier run) and always states the
// boundary: the session waits or asks the operator; it cannot trigger a review.
func reviewHint(states []string) string {
	labels := map[string]string{
		reviewStateRunning:    "进行中，等待即可",
		reviewStateQueued:     "排队中，等待即可",
		reviewStateCooling:    "冷却中，稍后自动重试",
		reviewStatePaused:     "已暂停，请操作员继续复核",
		reviewStateFailed:     "复核未通过，需补证据或修正断言后重试",
		reviewStateDisabled:   "未启用或未开启自动复核，请操作员处理",
		reviewStateIneligible: "不可独立复核，不能作为必要前提",
	}
	order := []string{
		reviewStateRunning, reviewStateQueued, reviewStateCooling,
		reviewStatePaused, reviewStateFailed, reviewStateDisabled, reviewStateIneligible,
	}
	counts := map[string]int{}
	for _, state := range states {
		counts[state]++
	}
	parts := make([]string, 0, len(order))
	for _, state := range order {
		if n := counts[state]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d 条%s", n, labels[state]))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "前提复核：" + strings.Join(parts, "；") + "。复核由控制面/操作员触发，会话不能触发复核。"
}

func (s *Server) getIntentReadiness(w http.ResponseWriter, r *http.Request) {
	db := s.Store.Snapshot()
	for _, in := range db.Intents {
		if in.ID == r.PathValue("id") {
			out := s.readiness(db, in, s.premisePolicy(), r.URL.Query().Get("verify_bytes") == "1")
			if harnessSession(r) {
				for i := range out.Premises {
					if out.Premises[i].Status != "verified" {
						out.Premises[i].Title = ""
					}
				}
				out.SelfReferenceHint = ""
			}
			writeJSON(w, 200, out)
			return
		}
	}
	writeErr(w, 404, "意图不存在")
}
