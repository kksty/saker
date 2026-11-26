package api

import (
	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

const (
	operatorParkedLimit  = 5
	operatorPremiseLimit = 4
)

type operatorPremiseIssue struct {
	FactID string `json:"fact_id"`
	Reason string `json:"reason"`
}

type operatorFrontierItem struct {
	board.FrontierItem
	Held                 bool                   `json:"held,omitempty"`
	PremiseIssues        []operatorPremiseIssue `json:"premise_issues,omitempty"`
	OmittedPremiseIssues int                    `json:"omitted_premise_issues,omitempty"`
	// NoEvidenceFacts names premises whose original evidence is missing or
	// damaged. It is an annotation, not a blocker: evidence is advisory for
	// admission, so the direction can still dispatch.
	NoEvidenceFacts []string `json:"no_evidence_facts,omitempty"`
}

// operatorFrontier adds an operator-only view of prerequisite metadata and
// parked directions. It never changes the frontier seen by a session or used by
// the scheduler. Original files are checked separately at dispatch.
func (s *Server) operatorFrontier(db store.DB, frontier []board.FrontierItem) []operatorFrontierItem {
	intents := make(map[string]model.Intent, len(db.Intents))
	for _, in := range db.Intents {
		intents[in.ID] = in
	}

	inputs := newReadinessInputs(db)
	policy := s.premisePolicy()
	out := make([]operatorFrontierItem, 0, len(frontier)+operatorParkedLimit)
	appendRow := func(f board.FrontierItem, in model.Intent) {
		row := operatorFrontierItem{FrontierItem: f, Held: in.Hold}
		if len(in.FromFactIDs) > 0 {
			ready := s.readinessFromInputs(inputs, in, policy, false)
			row.Ready = row.Ready && ready.Ready
			for _, p := range ready.Premises {
				if fact, ok := inputs.facts[p.ID]; ok && len(fact.EvidenceIDs) == 0 && len(row.NoEvidenceFacts) < operatorPremiseLimit {
					row.NoEvidenceFacts = append(row.NoEvidenceFacts, p.ID)
				}
			}
			for _, p := range ready.Premises {
				if p.Usable {
					continue
				}
				if len(row.PremiseIssues) == operatorPremiseLimit {
					row.OmittedPremiseIssues++
					continue
				}
				row.PremiseIssues = append(row.PremiseIssues, operatorPremiseIssue{
					FactID: p.ID, Reason: p.Reason,
				})
			}
		}
		out = append(out, row)
	}
	for _, f := range frontier {
		appendRow(f, intents[f.ID])
	}
	for i, parked := len(db.Intents)-1, 0; i >= 0 && parked < operatorParkedLimit; i-- {
		in := db.Intents[i]
		if in.Status != model.StatusParked {
			continue
		}
		appendRow(board.FrontierItem{
			ID: in.ID, Title: in.Title, Status: in.Status,
			Prerequisites: len(in.FromFactIDs), NoPremise: len(in.FromFactIDs) == 0,
		}, in)
		parked++
	}
	return out
}

// evidencePreserved used to live here. It was never called: the advisory
// annotation above is computed from len(EvidenceIDs) == 0, and the dispatch-time
// record lives in store.PrepareRunInput (which notes a digest gap instead of
// refusing). Keeping an unused helper in this file made it look as if the
// frontier enforced digest integrity, which it does not.
