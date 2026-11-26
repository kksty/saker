package store

// A proposal is an executor's structured suggestion for the next intent. It is
// control state, not an observation: the ledger row records the suggester's
// request, the commander/operator decision, and the intent/run the approval
// produced. Every transition is in the version history, so the row itself only
// carries the current state and the CAS version.
//
// The store owns two invariants the API cannot: idempotent creation per
// (proposer, key), and an approval that commits the pending intent and the
// approved status in one transaction. A second transaction there could crash
// between them and leave "approved with no intent", or an intent nobody decided.

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kksty/saker/internal/model"
)

var (
	// ErrProposalInvalid means the request is incomplete or contradictory: a
	// missing id/key/author, an empty rejection reason, or an approve with no
	// usable work. It is the caller's fix, not a lost race.
	ErrProposalInvalid = errors.New("提议请求不完整")
	// ErrProposalNotFound means the id is unknown in this project's ledger.
	ErrProposalNotFound = errors.New("提议不存在")
	// ErrProposalConflict means the caller acted on a version/status that has
	// moved: it must re-read the proposal and decide on the current one.
	ErrProposalConflict = errors.New("提议状态或版本已变化")
	// ErrProposalForbidden means the caller is not the proposer. Withdrawal is
	// the proposer's own undo; a commander/reject decision is a different path.
	ErrProposalForbidden = errors.New("只有提议者可以撤回这条提议")
)

// CreateProposal stores one proposed next step. The id, idempotency key and
// proposer are required: the id is the address the decision endpoints use, and
// the key is how a transport retry resolves to the same row instead of a second
// suggestion. A key reused with different content is a conflict, never a silent
// overwrite.
func (s *Store) CreateProposal(in model.Proposal) (model.Proposal, bool, error) {
	in.ID = strings.TrimSpace(in.ID)
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	in.FromParticipantID = strings.TrimSpace(in.FromParticipantID)
	if in.ID == "" || in.IdempotencyKey == "" || in.FromParticipantID == "" {
		return model.Proposal{}, false, fmt.Errorf("%w：id、idempotency_key 与提议者身份都必填", ErrProposalInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The retry key is checked first: a caller that retried with the same id it
	// generated the first time is still the same request, not a collision.
	for i := range s.db.Proposals {
		existing := s.db.Proposals[i]
		if existing.FromParticipantID != in.FromParticipantID || existing.IdempotencyKey != in.IdempotencyKey {
			continue
		}
		if !sameProposalRequest(existing, in) {
			// ErrIdempotencyConflict is the ledger-wide "same key, different
			// content" error (message.go); the API maps it to a proposal code so
			// the caller can tell it from a version conflict.
			return model.Proposal{}, false, fmt.Errorf("%w：%s", ErrIdempotencyConflict, in.IdempotencyKey)
		}
		return detachProposal(existing), false, nil
	}
	for i := range s.db.Proposals {
		if s.db.Proposals[i].ID == in.ID {
			return model.Proposal{}, false, fmt.Errorf("%w：提议 id 已存在（%s）", ErrProposalConflict, in.ID)
		}
	}
	now := time.Now()
	in.Status = model.ProposalProposed
	in.Version = 1
	in.DecidedBy, in.DecisionNote, in.DecidedAt = "", "", nil
	in.IntentID, in.RunID = "", ""
	in.CreatedAt, in.UpdatedAt = now, now
	in = detachProposal(in)
	s.db.Proposals = append(s.db.Proposals, in)
	return detachProposal(in), true, s.flush("proposal")
}

// sameProposalRequest compares the caller-controlled work description. Decision
// provenance and ledger timestamps are not part of it: a retry of the same
// suggestion is the same request after the first one was decided.
func sameProposalRequest(a, b model.Proposal) bool {
	return a.Title == b.Title &&
		a.Description == b.Description &&
		a.Doc == b.Doc &&
		a.Eval == b.Eval &&
		a.Rationale == b.Rationale &&
		slices.Equal(a.FromFactIDs, b.FromFactIDs) &&
		slices.Equal(a.ContextFactIDs, b.ContextFactIDs) &&
		slices.Equal(a.PinnedFactIDs, b.PinnedFactIDs) &&
		slices.Equal(a.Skills, b.Skills) &&
		a.Node == b.Node &&
		a.MaxTurns == b.MaxTurns
}

// Proposals returns detached proposals, newest first. The caller decides its own
// scope (an executor sees only its own; the commander/operator sees the queue).
func (s *Store) Proposals() []model.Proposal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Proposal, len(s.db.Proposals))
	for i := range s.db.Proposals {
		out[i] = detachProposal(s.db.Proposals[i])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Store) GetProposal(id string) (model.Proposal, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.db.Proposals {
		if p.ID == id {
			return detachProposal(p), true
		}
	}
	return model.Proposal{}, false
}

// DecideProposal approves or rejects one proposed next step.
//
// Approval constructs a pending intent from the proposed fields and commits it
// together with the approved status in one flush, through the same
// createIntentLocked path CreateIntent uses (initial context validation, premise
// claim snapshot). A failed write therefore leaves neither an approved row
// without an intent nor an intent nobody decided.
//
// Rejection requires a reason: "驳回" without why gives the proposer nothing to
// do next, and the decision note is the only place it can be answered.
func (s *Store) DecideProposal(id string, expectedVersion uint64, approve bool, note, by string) (model.Proposal, model.Intent, error) {
	note = strings.TrimSpace(note)
	by = strings.TrimSpace(by)
	if by == "" {
		return model.Proposal{}, model.Intent{}, fmt.Errorf("%w：决定人不能为空", ErrProposalInvalid)
	}
	if !approve && note == "" {
		return model.Proposal{}, model.Intent{}, fmt.Errorf("%w：驳回必须写理由", ErrProposalInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Proposals {
		p := &s.db.Proposals[i]
		if p.ID != id {
			continue
		}
		if p.Status != model.ProposalProposed || p.Version != expectedVersion {
			return model.Proposal{}, model.Intent{}, fmt.Errorf("%w：提议当前为 %s v%d（请求 v%d）",
				ErrProposalConflict, p.Status, p.Version, expectedVersion)
		}
		now := time.Now()
		if !approve {
			p.Status = model.ProposalRejected
			p.DecidedBy, p.DecisionNote, p.DecidedAt = by, note, &now
			p.Version++
			p.UpdatedAt = now
			return detachProposal(*p), model.Intent{}, s.flush("proposal")
		}
		in, err := s.createIntentLocked(model.Intent{
			Title:          p.Title,
			Description:    p.Description,
			Doc:            p.Doc,
			Eval:           p.Eval,
			FromFactIDs:    p.FromFactIDs,
			ContextFactIDs: p.ContextFactIDs,
			PinnedFactIDs:  p.PinnedFactIDs,
			Skills:         p.Skills,
			Node:           p.Node,
			MaxTurns:       p.MaxTurns,
			CreatedBy:      by,
		})
		if err != nil {
			return model.Proposal{}, model.Intent{}, err
		}
		p.Status = model.ProposalApproved
		p.DecidedBy, p.DecisionNote, p.DecidedAt = by, note, &now
		p.IntentID = in.ID
		p.Version++
		p.UpdatedAt = now
		return detachProposal(*p), in, s.flush("proposal", "intent")
	}
	return model.Proposal{}, model.Intent{}, fmt.Errorf("%w：%s", ErrProposalNotFound, id)
}

// WithdrawProposal is the proposer's own undo, allowed only while nobody has
// decided. The by check is part of the store contract: the idempotency key is
// per proposer, and a withdrawal is attributed to the proposer, not to whichever
// caller happens to reach the handler.
func (s *Store) WithdrawProposal(id string, expectedVersion uint64, by string) (model.Proposal, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return model.Proposal{}, fmt.Errorf("%w：撤回人不能为空", ErrProposalInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Proposals {
		p := &s.db.Proposals[i]
		if p.ID != id {
			continue
		}
		if p.FromParticipantID != by {
			return model.Proposal{}, fmt.Errorf("%w（提议者 %s）", ErrProposalForbidden, p.FromParticipantID)
		}
		if p.Status != model.ProposalProposed || p.Version != expectedVersion {
			return model.Proposal{}, fmt.Errorf("%w：提议当前为 %s v%d（请求 v%d）",
				ErrProposalConflict, p.Status, p.Version, expectedVersion)
		}
		now := time.Now()
		p.Status = model.ProposalWithdrawn
		p.DecidedBy, p.DecidedAt = by, &now
		p.Version++
		p.UpdatedAt = now
		return detachProposal(*p), s.flush("proposal")
	}
	return model.Proposal{}, fmt.Errorf("%w：%s", ErrProposalNotFound, id)
}

// Dispatch commits its receipt through StartProposalRun (admission.go), in the
// same transaction as the intent claim and run row. There is deliberately no
// separate "mark dispatched" write: a second path is how a run could exist while
// its proposal still read approved.
