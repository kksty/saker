package store

import (
	"encoding/json"
	"slices"

	"github.com/kksty/saker/internal/model"
)

// RoundProgress records evidence and intent transitions since the previous counted round.
type RoundProgress struct {
	Rounds        int
	Escalate      bool
	NewFacts      int
	NewProofs     int
	NewIntents    int
	ClosedIntents int
	// Intent changes accumulated across the current no-progress episode.
	Opened       int
	Parked       int
	Failed       int
	ReviewFailed int
}

// ObserveDecisionRound records one decision round against the journal watermark
// the previous round consumed. The watermark and the counter live in Memory,
// not in the runner, so a restart preserves the count. It writes through
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
	firstProofs := make(map[string]struct{})
	facts := make(map[string]model.Fact, len(s.db.Facts))
	for _, f := range s.db.Facts {
		facts[f.ID] = f
	}
	// Reviews are appended in ledger order. Find the first qualifying receipt
	// for each current claim, even if it predates this round's watermark.
	proved := make(map[string]bool)
	for i := range s.db.Reviews {
		review := s.db.Reviews[i]
		if proved[review.FactID] {
			continue
		}
		if _, ok := model.CurrentProof(facts[review.FactID], s.db.Reviews[i:i+1]); ok {
			proved[review.FactID] = true
			firstProofs[review.ID] = struct{}{}
		}
	}
	for _, c := range mem.History {
		if c.Seq <= mem.StallThrough {
			continue
		}
		switch {
		case c.Kind == "fact" && c.Operation == "create":
			out.NewFacts++
		case c.Kind == "intent" && c.Operation == "create":
			out.NewIntents++
		case c.Kind == "review" && c.Operation == "create":
			if _, ok := firstProofs[c.ID]; ok {
				out.NewProofs++
			}
		case c.Kind == "intent" && c.Operation == "update" && slices.Contains(c.Fields, "status"):
			var row model.Intent
			if json.Unmarshal(c.Value, &row) != nil {
				continue
			}
			switch row.Status {
			case model.StatusDone:
				out.ClosedIntents++
			case model.StatusParked:
				mem.StallParked++
			case model.StatusFailed:
				mem.StallFailed++
			case model.StatusReviewFailed:
				mem.StallReviewFailed++
			}
		}
	}
	progressed := out.NewFacts+out.NewProofs+out.ClosedIntents > 0
	if progressed {
		mem.StallRounds = 0
		mem.StallDirections = 0
		mem.StallParked, mem.StallFailed, mem.StallReviewFailed = 0, 0, 0
		// A stall episode ends here: the next dry run starts a fresh episode
		// and may escalate again once it has earned a full limit.
		mem.StallEscalated = false
	} else {
		mem.StallRounds++
		mem.StallDirections += out.NewIntents
	}
	out.Opened = mem.StallDirections
	out.Parked, out.Failed, out.ReviewFailed = mem.StallParked, mem.StallFailed, mem.StallReviewFailed
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
