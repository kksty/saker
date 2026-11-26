package store

// Proposal receipts written before the dispatch transaction existed can be
// missing even though a run did start: the old flow committed StartRun first
// and wrote MarkProposalDispatched second, so a crash in between left an
// approved proposal beside a live (or settled) run. This repair fills only the
// unambiguous rows. "Unambiguous" means the approved proposal's intent has
// exactly one intent run that started at/after the approval and no other
// proposal already claims it; anything else keeps a reason instead of guessing.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kksty/saker/internal/model"
)

// Proposal receipt plan states.
const (
	ProposalReceiptFill      = "fill"
	ProposalReceiptAmbiguous = "ambiguous"
	ProposalReceiptMissing   = "missing"
	ProposalReceiptSkipped   = "skipped"
)

// ProposalReceiptPlan is one historical proposal's repairability. Status is fill
// only when the run is uniquely attributable; every other row carries Reason
// and is left untouched.
type ProposalReceiptPlan struct {
	ProposalID string `json:"proposal_id"`
	IntentID   string `json:"intent_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
}

// PlanProposalReceipts inspects a committed ledger clone and proposes receipt
// repairs. It never changes db.
func PlanProposalReceipts(db DB) []ProposalReceiptPlan {
	intents := make(map[string]model.Intent, len(db.Intents))
	runsByIntent := make(map[string][]model.Run, len(db.Intents))
	for _, in := range db.Intents {
		intents[in.ID] = in
	}
	for _, r := range db.Runs {
		if r.Purpose == "" && r.IntentID != "" {
			runsByIntent[r.IntentID] = append(runsByIntent[r.IntentID], r)
		}
	}
	claimed := map[string]string{}
	for _, p := range db.Proposals {
		if p.RunID != "" {
			claimed[p.RunID] = p.ID
		}
	}
	out := make([]ProposalReceiptPlan, 0, len(db.Proposals))
	for _, p := range db.Proposals {
		if p.RunID != "" || (p.Status != model.ProposalApproved && p.Status != model.ProposalDispatched) {
			continue
		}
		plan := ProposalReceiptPlan{ProposalID: p.ID, IntentID: p.IntentID}
		if p.IntentID == "" {
			plan.Status, plan.Reason = ProposalReceiptSkipped, "已批准但没有产生意图，无法归属运行"
			out = append(out, plan)
			continue
		}
		if _, ok := intents[p.IntentID]; !ok {
			plan.Status, plan.Reason = ProposalReceiptSkipped, "意图不在账本中"
			out = append(out, plan)
			continue
		}
		approvedAt := p.UpdatedAt
		if p.DecidedAt != nil {
			approvedAt = *p.DecidedAt
		}
		var candidates []model.Run
		for _, r := range runsByIntent[p.IntentID] {
			if r.StartedAt.Before(approvedAt) {
				continue
			}
			if owner, ok := claimed[r.ID]; ok && owner != p.ID {
				continue
			}
			candidates = append(candidates, r)
		}
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].StartedAt.Before(candidates[j].StartedAt) })
		switch len(candidates) {
		case 1:
			plan.Status, plan.RunID = ProposalReceiptFill, candidates[0].ID
		case 0:
			plan.Status, plan.Reason = ProposalReceiptMissing, "批准后没有可归属的意图运行"
		default:
			ids := make([]string, 0, len(candidates))
			for _, r := range candidates {
				ids = append(ids, r.ID)
			}
			plan.Status = ProposalReceiptAmbiguous
			plan.Reason = "批准后有 " + fmt.Sprint(len(candidates)) + " 个候选运行，无法唯一归属：" + strings.Join(ids, ", ")
		}
		out = append(out, plan)
	}
	return out
}

// ApplyProposalReceipts fills every plan with an unambiguous run. It is a single
// transaction: a failure leaves all proposals exactly as they were, so the
// operator can re-run the preview and retry. The returned plans are the same
// preview the caller should show (including the skipped reasons).
func (s *Store) ApplyProposalReceipts() ([]ProposalReceiptPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plans := PlanProposalReceipts(s.db)
	if len(plans) == 0 {
		return plans, nil
	}
	now := time.Now()
	changed := false
	for _, plan := range plans {
		if plan.Status != ProposalReceiptFill {
			continue
		}
		for i := range s.db.Proposals {
			p := &s.db.Proposals[i]
			if p.ID != plan.ProposalID {
				continue
			}
			if p.RunID != "" || p.IntentID != plan.IntentID {
				return plans, fmt.Errorf("提议 %s 在修复期间已被改写，请重新检查", p.ID)
			}
			p.Status = model.ProposalDispatched
			p.RunID = plan.RunID
			p.Version++
			p.UpdatedAt = now
			changed = true
			break
		}
	}
	if !changed {
		return plans, nil
	}
	if err := s.flush("proposal"); err != nil {
		return plans, err
	}
	return plans, nil
}
