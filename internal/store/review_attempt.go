package store

import (
	"sort"
	"strings"
	"time"
)

// ReviewAttempt outcomes. They describe why the automatic review queue is not
// moving a finding; they are never a verdict and never change the fact's status.
const (
	// ReviewAttemptRunFailed is a review run that did not finish normally (link,
	// timeout, non-zero exit). It is retried after a short cooldown.
	ReviewAttemptRunFailed = "run_failed"
	// ReviewAttemptNeedsEvidence is a finished review whose verdict was
	// needs_evidence. It waits for the assertion or its evidence set to change;
	// retrying the same material would only buy the same answer again.
	ReviewAttemptNeedsEvidence = "needs_evidence"
	// ReviewAttemptRejected is an independent review's reject verdict. It is
	// recorded so the console can show it separately from a broken run or
	// missing material; the verdict itself lives in the Review row.
	ReviewAttemptRejected = "rejected"
)

// ReviewAttempt is the durable last attempt at one fact's assertion version. It
// is queue bookkeeping stored in Memory, not a ledger row: no fact, review or
// report path reads it as evidence. The claim version is part of the record so a
// corrected assertion does not inherit the old attempt's cooldown or block.
type ReviewAttempt struct {
	FactID       string `json:"fact_id"`
	ClaimVersion uint64 `json:"claim_version"`
	ClaimHash    string `json:"claim_hash,omitempty"`
	Outcome      string `json:"outcome"`
	// Reason is the operator-readable cause: the run error, the verifier's notes
	// for needs_evidence, or the reject notes.
	Reason string `json:"reason,omitempty"`
	// RunID is the review run that produced the outcome. Empty when the attempt
	// was recorded without a run (e.g. an eligibility check).
	RunID string `json:"run_id,omitempty"`
	// EvidenceIDs freezes the evidence set the attempt judged. A later evidence
	// addition or removal is a material change and makes the attempt stale, so
	// the queue may try again without waiting for a new assertion version.
	EvidenceIDs []string  `json:"evidence_ids,omitempty"`
	FailedAt    time.Time `json:"failed_at"`
	// RetryAt is when a transient failure may be retried. Nil means the attempt
	// is blocked until the claim or evidence changes, or the outcome is a
	// terminal verdict (rejected).
	RetryAt *time.Time `json:"retry_at,omitempty"`
}

// SameMaterial reports whether the attempt still describes a fact with this
// assertion version and evidence set. Identity is not the title.
func (a ReviewAttempt) SameMaterial(claimVersion uint64, evidenceIDs []string) bool {
	if a.ClaimVersion != claimVersion {
		return false
	}
	return sameEvidenceSet(a.EvidenceIDs, evidenceIDs)
}

func sameEvidenceSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, id := range a {
		seen[id]++
	}
	for _, id := range b {
		seen[id]--
		if seen[id] < 0 {
			return false
		}
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// ReviewAttemptFor returns the last attempt at one fact, if any. The returned
// slice is detached from the ledger's memory map.
func (s *Store) ReviewAttemptFor(factID string) (ReviewAttempt, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db.Memory == nil {
		return ReviewAttempt{}, false
	}
	a, ok := s.db.Memory.ReviewAttempts[factID]
	if !ok {
		return ReviewAttempt{}, false
	}
	return detachReviewAttempt(a), true
}

// ReviewAttempts returns every durable attempt, sorted by fact id. The API uses
// it for the operator's bounded summary; it is a copy, never the live map.
func (s *Store) ReviewAttempts() []ReviewAttempt {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db.Memory == nil || len(s.db.Memory.ReviewAttempts) == 0 {
		return nil
	}
	out := make([]ReviewAttempt, 0, len(s.db.Memory.ReviewAttempts))
	for _, a := range s.db.Memory.ReviewAttempts {
		out = append(out, detachReviewAttempt(a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FactID < out[j].FactID })
	return out
}

// RecordReviewAttempt stores the last attempt for one fact. It replaces the
// previous row for that fact: "last failure" is the question, not a history.
// It flushes as bookkeeping, like the review cursor.
func (s *Store) RecordReviewAttempt(a ReviewAttempt) error {
	if a.FactID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mem := Memory{Epoch: s.readEpoch, Checkpoints: map[string]Checkpoint{}}
	if s.db.Memory != nil {
		mem = *s.db.Memory
	}
	mem.ReviewAttempts = map[string]ReviewAttempt{}
	if s.db.Memory != nil {
		for k, v := range s.db.Memory.ReviewAttempts {
			mem.ReviewAttempts[k] = v
		}
	}
	a = detachReviewAttempt(a)
	mem.ReviewAttempts[a.FactID] = a
	s.db.Memory = &mem
	return s.flushNoRev("memory")
}

// ClearReviewAttempt drops the attempt only when it belongs to the given claim
// version. A slower worker that settled an older claim must not erase the newer
// version's attempt.
func (s *Store) ClearReviewAttempt(factID string, claimVersion uint64) error {
	if factID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.Memory == nil || s.db.Memory.ReviewAttempts == nil {
		return nil
	}
	current, ok := s.db.Memory.ReviewAttempts[factID]
	if !ok || current.ClaimVersion != claimVersion {
		return nil
	}
	mem := *s.db.Memory
	mem.ReviewAttempts = map[string]ReviewAttempt{}
	for k, v := range s.db.Memory.ReviewAttempts {
		if k != factID {
			mem.ReviewAttempts[k] = v
		}
	}
	s.db.Memory = &mem
	return s.flushNoRev("memory")
}

func detachReviewAttempt(a ReviewAttempt) ReviewAttempt {
	a.EvidenceIDs = append([]string(nil), a.EvidenceIDs...)
	if a.RetryAt != nil {
		at := *a.RetryAt
		a.RetryAt = &at
	}
	if a.Reason != "" {
		a.Reason = strings.TrimSpace(a.Reason)
	}
	return a
}
