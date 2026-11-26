package store

// The automatic review queue is an enumeration, not a top-N. A finding can be
// pushed out of "the newest 50 facts" by notes and host observations; that must
// not mean it never gets a second look (F11). This file owns the eligible set,
// its stable ring order, and the durable per-intent resume cursor. The caller
// owns the review switch, the concurrency admission and the retry cooldown; the
// store only answers "which candidates exist and where were we".

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kksty/saker/internal/model"
)

// ReviewEligible decides what belongs in the automatic review queue: the same
// findings a report would carry, while they are still unverified. verifier.Eligible
// delegates here so the queue and the verifier's admission can never disagree
// (verifier imports store; store must not import verifier).
func ReviewEligible(f model.Fact) bool {
	if f.Status != "unverified" {
		return false
	}
	switch f.Kind {
	case "vuln", "cred":
		return true
	}
	return false
}

// ReviewPassBlocked reports why a passing verdict cannot stand, or "" when it
// may set the fact to verified. A finding the report carries as proof (vuln,
// cred) with no cited evidence has nothing an independent reviewer can replay:
// a pass would be a second model endorsing the writer's own text, which is the
// laundering the ledger exists to prevent.
//
// "Cited" is checked against the ledger, not against the length of the list: an
// id that names no evidence row is the same emptiness wearing a name, and a
// verdict on it would send the report a pointer to bytes nobody preserved. The
// rule is stated once and read by the write gate (Store.ReviewFact), the
// verifier's admission and the manual endpoint's refusal, so a run that cannot
// possibly pass is never bought and every reader says the same thing.
func (s *Store) ReviewPassBlocked(f model.Fact) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reviewPassBlockedLocked(f)
}

// reviewPassBlockedLocked is ReviewPassBlocked for callers that already hold the
// lock (the write gate does).
func (s *Store) reviewPassBlockedLocked(f model.Fact) string {
	switch f.Kind {
	case "vuln", "cred":
		if len(f.EvidenceIDs) == 0 {
			return fmt.Sprintf("结论「%s」没有任何原始证据：复核没有可重放的材料，不能写成已核验；先补证据（原始请求/响应/命令输出）再复核", f.Title)
		}
		known := make(map[string]bool, len(s.db.Evidence))
		for _, e := range s.db.Evidence {
			known[e.ID] = true
		}
		for _, id := range f.EvidenceIDs {
			if known[id] {
				continue
			}
			label := id
			if label == "" {
				label = "(空 id)"
			}
			return fmt.Sprintf("证据 %s 不在账本里：引用了一条不存在的原件（结论「%s」不能据此判 pass）", label, f.Title)
		}
	}
	return ""
}

// ReviewCandidates returns up to limit review candidates in stable ID order,
// starting strictly after afterID ("" starts at the smallest ID), and the cursor
// a following walk should resume after.
//
// The walk is contiguous and wraps at the tail: when fewer than limit candidates
// remain the batch returns just those and next is the ID of the last one
// returned, so the next call wraps to the ring head. Returned next is the
// "already handled" point, not the first untouched candidate: the caller must
// persist it only after the returned facts were actually processed, so a pause
// leaves the cursor on the last processed candidate and the next pass starts
// with the first untouched one.
//
// Every eligible fact is reachable: the order is by ID and the walk wraps, so a
// new note or host fact can push an old finding out of a "newest N" window but
// not out of the queue. A verified or rejected fact drops out of the set; a
// claim correction returns it to unverified (store.UpdateFact) and therefore
// back into the queue with no separate bookkeeping.
type ReviewPolicy struct {
	Mode         string
	RequiredOnly bool
}

func (s *Store) ReviewCandidates(intentID, afterID string, limit int, options ...ReviewPolicy) ([]model.Fact, string) {
	if limit <= 0 {
		return nil, afterID
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var policy ReviewPolicy
	if len(options) > 0 {
		policy = options[0]
	}
	needed := map[string]bool{}
	if policy.Mode == model.PremiseVerifiedOnly || policy.RequiredOnly {
		for _, in := range s.db.Intents {
			if model.Settled(in.Status) {
				continue
			}
			for _, id := range in.FromFactIDs {
				needed[id] = true
			}
		}
	}
	rows := make([]model.Fact, 0, len(s.db.Facts))
	// One pass over the reviews, then a lookup per fact. Calling
	// LatestReviewForClaim with the whole review slice scanned every review for
	// every fact — O(facts × reviews) while holding the ledger read lock — and the
	// console's board poll asks for the queue on every tick.
	reviewsByFact := make(map[string][]model.Review, len(s.db.Reviews))
	for _, r := range s.db.Reviews {
		reviewsByFact[r.FactID] = append(reviewsByFact[r.FactID], r)
	}
	for _, f := range s.db.Facts {
		if intentID != "" && f.IntentID != intentID {
			continue
		}
		eligible := ReviewEligible(f) || (policy.Mode == model.PremiseVerifiedOnly && needed[f.ID] && f.Status == "unverified" && model.ReviewableKind(f.Kind))
		if !eligible || (policy.RequiredOnly && !needed[f.ID]) {
			continue
		}
		// A needs_evidence verdict bound to this claim version is terminal for
		// the automatic queue: retrying the same bytes buys the same model run.
		// A corrected assertion (new version) has no matching review and is
		// eligible again; the operator can still trigger a manual retry.
		if r, ok := model.LatestReviewForClaim(f, reviewsByFact[f.ID]); ok && r.Verdict == "needs_evidence" {
			continue
		}
		rows = append(rows, detachFact(f))
	}
	if len(rows) == 0 {
		return nil, ""
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	start := sort.Search(len(rows), func(i int) bool { return rows[i].ID > afterID })
	if start == len(rows) {
		// The cursor is past the tail (or the rest of the ring left the set):
		// wrap to the beginning.
		start = 0
	}
	batch := rows[start:]
	if len(batch) > limit {
		batch = batch[:limit]
	}
	return batch, batch[len(batch)-1].ID
}

// ReviewCursor returns the stored resume point for one intent, "" when the walk
// has not started. It is durable ledger memory, not process state: a restart
// continues where the last pass stopped instead of re-reading the ring head.
func (s *Store) ReviewCursor(intentID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db.Memory == nil || intentID == "" {
		return ""
	}
	return s.db.Memory.ReviewCursors[intentID]
}

// SetReviewCursor records the resume point for one intent. It flushes as
// bookkeeping (flushNoRev): a cursor move is not a change to the fact graph and
// must not wake the commander.
func (s *Store) SetReviewCursor(intentID, nextID string) error {
	if strings.TrimSpace(intentID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mem := Memory{Epoch: s.readEpoch, Checkpoints: map[string]Checkpoint{}}
	if s.db.Memory != nil {
		mem = *s.db.Memory
	}
	mem.ReviewCursors = map[string]string{}
	if s.db.Memory != nil {
		for k, v := range s.db.Memory.ReviewCursors {
			mem.ReviewCursors[k] = v
		}
	}
	if nextID == "" {
		delete(mem.ReviewCursors, intentID)
	} else {
		mem.ReviewCursors[intentID] = nextID
	}
	s.db.Memory = &mem
	return s.flushNoRev("memory")
}
