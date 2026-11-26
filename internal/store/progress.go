package store

import (
	"encoding/json"

	"github.com/kksty/saker/internal/model"
)

// RoundProgress is one completed decision round's view of the durable journal:
// did the engagement move since the previous counted round, and how many
// consecutive rounds have moved nothing.
//
// It is the same journal differential the attention router reads
// (attention.go). Progress is deliberately narrow: a new fact or a direction
// reaching a settled status. A newly opened direction is observed (Directions /
// NewIntents) but is NOT progress: splitting one plan into more directions
// leaves the ledger exactly as dry as before, and counting it reset the counter
// in the live ledger, hiding stalls of six or more no-fact rounds. The alert
// text reports those directions instead, so the operator sees "opening
// directions, producing no facts" rather than a vague "no progress". Nothing
// here judges whether the work was useful; it only asks whether the evidence
// grew. A round that only chatted, updated a note or failed a tool is a
// no-progress round, which is exactly the silence this counter exists to expose.
type RoundProgress struct {
	Rounds        int // consecutive no-progress rounds, including this one
	Escalate      bool
	NewFacts      int // this round
	NewIntents    int // this round; observed, not progress
	ClosedIntents int // this round
	// Directions is how many directions were opened during the current dry
	// episode. At an escalation it is the number the operator-visible alert
	// reports, and it is reset only by real progress.
	Directions int
}

// ObserveDecisionRound records one decision round against the journal watermark
// the previous round consumed. The watermark and the counter live in Memory,
// not in the runner: a restart must resume the count instead of starting from
// zero and hiding a direction that has been idling for hours. It writes through
// flushNoRev because bookkeeping must not wake the commander (the same reason
// ReviewCursors and the attention checkpoint use it).
//
// The caller decides which rounds count. A handoff round deliberately
// dispatches nothing, and a round that never reached the model is not a
// decision, so neither may advance this counter (see decider.Runner).
func (s *Store) ObserveDecisionRound(limit int) (RoundProgress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mem := Memory{Epoch: s.readEpoch, Checkpoints: map[string]Checkpoint{}}
	if s.db.Memory != nil {
		mem = *s.db.Memory
	}
	out := RoundProgress{}
	for _, c := range mem.History {
		if c.Seq <= mem.StallThrough {
			continue
		}
		switch {
		case c.Kind == "fact" && c.Operation == "create":
			out.NewFacts++
		case c.Kind == "intent" && c.Operation == "create":
			out.NewIntents++
		case c.Kind == "intent" && c.Operation == "update" && intentClosed(c):
			out.ClosedIntents++
		}
	}
	// A new direction is not progress: it moves work around without adding
	// evidence, and in the live ledger it reset the counter during six-plus
	// round stretches with no new fact. It is still counted so the alert can
	// say how much reshuffling happened while the evidence stayed flat.
	progressed := out.NewFacts+out.ClosedIntents > 0
	if progressed {
		mem.StallRounds = 0
		mem.StallDirections = 0
		// A stall episode ends here: the next dry run starts a fresh episode
		// and may escalate again once it has earned a full limit.
		mem.StallEscalated = false
	} else {
		mem.StallRounds++
		mem.StallDirections += out.NewIntents
	}
	out.Directions = mem.StallDirections
	out.Rounds = mem.StallRounds
	if !progressed && limit > 0 && mem.StallRounds >= limit && !mem.StallEscalated {
		// One visible alert per stall episode: the flag is durable, so neither
		// the following round nor a restart repeats the same escalation.
		mem.StallEscalated = true
		out.Escalate = true
	}
	mem.StallThrough = mem.Seq
	s.db.Memory = &mem
	return out, s.flushNoRev("memory")
}

// StallRounds is the durable consecutive no-progress count. It is read when a
// runner starts so the console shows the reason a direction went quiet before
// the current process existed.
func (s *Store) StallRounds() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db.Memory == nil {
		return 0
	}
	return s.db.Memory.StallRounds
}

// intentClosed recognises the status transition itself, not a later edit of an
// already-settled intent: only an update that changed "status" may count, so a
// run-id fix on a finished direction does not fake progress for another round.
func intentClosed(c Change) bool {
	changed := false
	for _, f := range c.Fields {
		if f == "status" {
			changed = true
			break
		}
	}
	if !changed {
		return false
	}
	var row struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(c.Value, &row) != nil {
		return false
	}
	return model.Settled(row.Status)
}
