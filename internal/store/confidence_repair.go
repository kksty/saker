package store

import (
	"fmt"

	"github.com/kksty/saker/internal/model"
)

type ConfidenceChange struct {
	FactID string `json:"fact_id"`
	Before string `json:"before"`
	After  string `json:"after"`
	base   string
}

type ConfidenceRepair struct {
	Version ReadVersion        `json:"snapshot_version"`
	Changes []ConfidenceChange `json:"changes"`
	Blocked []string           `json:"would_change_claim"`
}

// PlanConfidenceRepair recomputes effective labels from a frozen ledger. A
// legacy row is only repairable if the recomputation preserves its claim hash.
func PlanConfidenceRepair(db DB) ConfidenceRepair {
	out := ConfidenceRepair{Changes: []ConfidenceChange{}, Blocked: []string{}}
	if db.Memory != nil {
		out.Version = ReadVersion{Epoch: db.Memory.Epoch, Seq: db.Memory.Seq}
	}
	facts := make(map[string]*model.Fact, len(db.Facts))
	for i := range db.Facts {
		f := &db.Facts[i]
		facts[f.ID] = f
	}
	premises := make(map[string][]string, len(db.Intents))
	for _, in := range db.Intents {
		premises[in.ID] = in.FromFactIDs
	}
	for _, f := range db.Facts {
		next := f
		_, unverified := firstUnverifiedPremise(f.ID, premises[f.IntentID], func(id string) *model.Fact { return facts[id] })
		setDerivedConfidence(&next, unverified)
		if f.Confidence == next.Confidence {
			continue
		}
		if claimHash(next) != f.ClaimHash {
			out.Blocked = append(out.Blocked, f.ID)
			continue
		}
		out.Changes = append(out.Changes, ConfidenceChange{f.ID, f.Confidence, next.Confidence, next.DeclaredConfidence})
	}
	return out
}

// RepairConfidence is the explicit offline write path. The assertion and its
// review receipts remain bound to the original version and hash.
func (s *Store) RepairConfidence(expected ReadVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan := PlanConfidenceRepair(s.db)
	if expected.Epoch == "" || plan.Version != expected {
		return fmt.Errorf("账本已变化，请重新预览置信度修复")
	}
	if len(plan.Blocked) > 0 {
		return fmt.Errorf("有 %d 条事实需单独核对断言，置信度未修复", len(plan.Blocked))
	}
	if len(plan.Changes) == 0 {
		return nil
	}
	changes := make(map[string]ConfidenceChange, len(plan.Changes))
	for _, c := range plan.Changes {
		changes[c.FactID] = c
	}
	for i := range s.db.Facts {
		f := &s.db.Facts[i]
		if c, ok := changes[f.ID]; ok {
			f.Confidence, f.DeclaredConfidence = c.After, c.base
		}
	}
	return s.flush("fact")
}
