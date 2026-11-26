package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
)

// proposalFixture is one well-formed suggestion. Callers override the fields
// they are testing; the idempotency key is derived from the id so two fixtures
// with different ids are different requests unless a test says otherwise.
// Fact references are optional: a fixture with none is a genuine first touch.
func proposalFixture(id string) model.Proposal {
	return model.Proposal{
		ID:                id,
		Title:             "验证未授权接口",
		Description:       "确认 /api/admin 能否在未登录下读取，用最小请求复现并保存响应",
		Eval:              "能复现一次未授权读取并留住响应原文",
		Rationale:         "上一轮已确认管理端可达",
		Skills:            []string{"collaborate"},
		Node:              "local",
		MaxTurns:          10,
		FromParticipantID: "pt_in_x",
		FromIntentID:      "in_x",
		FromRunID:         "r_x",
		FromGeneration:    1,
		IdempotencyKey:    "k-" + id,
	}
}

func TestProposalCreateRequiresIDKeyAndAuthor(t *testing.T) {
	s := open(t)
	for i, in := range []model.Proposal{
		{IdempotencyKey: "k", FromParticipantID: "pt_in_x"},
		{ID: "pr_1", FromParticipantID: "pt_in_x"},
		{ID: "pr_1", IdempotencyKey: "k"},
	} {
		if _, _, err := s.CreateProposal(in); !errors.Is(err, ErrProposalInvalid) {
			t.Fatalf("case %d: err = %v, want ErrProposalInvalid", i, err)
		}
	}
	if got := s.Proposals(); len(got) != 0 {
		t.Fatalf("invalid requests left rows: %+v", got)
	}
}

// A transport retry of the same suggestion must resolve to the same row: the
// key is per proposer, and reusing it for different work is a conflict rather
// than an overwrite (a silent overwrite would let a retry change what the
// commander is deciding about).
func TestProposalCreationIsIdempotentPerProposerKey(t *testing.T) {
	s := open(t)
	first, created, err := s.CreateProposal(proposalFixture("pr_1"))
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if first.Status != model.ProposalProposed || first.Version != 1 || first.CreatedAt.IsZero() {
		t.Fatalf("created row = %+v", first)
	}
	retry := proposalFixture("pr_2")
	retry.IdempotencyKey = "k-pr_1"
	again, created, err := s.CreateProposal(retry)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("retry: created=%v id=%s err=%v, want the original %s", created, again.ID, err, first.ID)
	}
	if n := len(s.Proposals()); n != 1 {
		t.Fatalf("proposals = %d, want 1", n)
	}
	// Retrying with the very same id (a caller-side generated address) is still
	// the same request, not an id collision.
	sameID := proposalFixture("pr_1")
	if again, created, err := s.CreateProposal(sameID); err != nil || created || again.ID != first.ID {
		t.Fatalf("same-id retry: created=%v id=%s err=%v", created, again.ID, err)
	}
	different := retry
	different.ID = "pr_3"
	different.Title = "另一件事"
	if _, _, err := s.CreateProposal(different); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("same key different content: err = %v, want ErrIdempotencyConflict", err)
	}
	// A duplicate id under another key is a collision, not a new suggestion.
	collision := proposalFixture("pr_1")
	collision.IdempotencyKey = "k-other"
	if _, _, err := s.CreateProposal(collision); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("duplicate id: err = %v, want ErrProposalConflict", err)
	}
}

// A proposal is control state. It must not enter the content fingerprint that
// the report/board use to page "the world changed", but its decisions must stay
// in the version journal: a lost decision is exactly what this row exists to
// avoid (D86/D79 split between news and history).
func TestProposalIsHistoryNotContent(t *testing.T) {
	s := open(t)
	if _, err := s.CreateIntent(model.Intent{Title: "已有方向"}); err != nil {
		t.Fatal(err)
	}
	beforeHash, beforeRev := s.ContentHash(), s.Revision()
	p, _, err := s.CreateProposal(proposalFixture("pr_1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.ContentHash(); got != beforeHash {
		t.Fatalf("proposal changed the content fingerprint: %s -> %s", beforeHash, got)
	}
	if s.Revision() == beforeRev {
		t.Fatal("a new proposal must wake the decider: revision did not move")
	}
	history := s.History("proposal", p.ID)
	if len(history) != 1 || history[0].Operation != "create" || history[0].Version != 1 {
		t.Fatalf("proposal history = %+v", history)
	}
	if _, _, err := s.DecideProposal(p.ID, p.Version, false, "不采纳", "operator"); err != nil {
		t.Fatal(err)
	}
	if history := s.History("proposal", p.ID); len(history) != 2 || history[1].Operation != "update" {
		t.Fatalf("decision was not journaled: %+v", history)
	}
}

func TestProposalDecisionCASAndRejection(t *testing.T) {
	s := open(t)
	p, _, err := s.CreateProposal(proposalFixture("pr_1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DecideProposal(p.ID, p.Version+1, false, "重复", "operator"); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("stale version: err = %v, want ErrProposalConflict", err)
	}
	if _, _, err := s.DecideProposal(p.ID, p.Version, false, "   ", "operator"); !errors.Is(err, ErrProposalInvalid) {
		t.Fatalf("empty rejection reason: err = %v, want ErrProposalInvalid", err)
	}
	rejected, intent, err := s.DecideProposal(p.ID, p.Version, false, "与本轮授权重叠，先做完原意图", "operator")
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != model.ProposalRejected || rejected.Version != 2 || rejected.DecidedBy != "operator" ||
		rejected.DecisionNote == "" || rejected.DecidedAt == nil || rejected.IntentID != "" || intent.ID != "" {
		t.Fatalf("rejected = %+v intent=%+v", rejected, intent)
	}
	// A rejected suggestion is final: a later approval must not resurrect it.
	if _, _, err := s.DecideProposal(p.ID, rejected.Version, true, "", "commander"); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("approve after reject: err = %v, want ErrProposalConflict", err)
	}
	if _, err := s.WithdrawProposal(p.ID, rejected.Version, "pt_in_x"); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("withdraw after reject: err = %v, want ErrProposalConflict", err)
	}
	if _, err := s.MarkProposalDispatched(p.ID, rejected.Version, "r_1", "operator"); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("dispatch after reject: err = %v, want ErrProposalConflict", err)
	}
}

// Approval is the whole point of the entity: one transaction turns the row into
// approved AND creates the pending intent. The intent must be pending with no
// run — approval is not dispatch.
func TestProposalApprovalCreatesPendingIntentInOneTransaction(t *testing.T) {
	s := open(t)
	fact, err := s.AddFact(model.Fact{Kind: "vuln", Title: "管理端可达"})
	if err != nil {
		t.Fatal(err)
	}
	base := proposalFixture("pr_1")
	base.FromFactIDs = []string{fact.ID}
	base.ContextFactIDs = []string{fact.ID}
	base.PinnedFactIDs = []string{fact.ID}
	p, _, err := s.CreateProposal(base)
	if err != nil {
		t.Fatal(err)
	}
	approved, intent, err := s.DecideProposal(p.ID, p.Version, true, "值得做", "commander")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != model.ProposalApproved || approved.Version != 2 || approved.IntentID != intent.ID {
		t.Fatalf("approved = %+v intent=%+v", approved, intent)
	}
	if intent.Status != model.StatusPending || intent.CreatedBy != "commander" || intent.Title != p.Title {
		t.Fatalf("created intent = %+v", intent)
	}
	if intent.FromFactClaims[fact.ID] != fact.ClaimVersion {
		t.Fatalf("premise snapshot = %+v, want claim v%d", intent.FromFactClaims, fact.ClaimVersion)
	}
	if intent.ContextFactIDs[0] != fact.ID || intent.PinnedFactIDs[0] != fact.ID {
		t.Fatalf("context/pinned lost: %+v", intent)
	}
	if got := s.Runs(); len(got) != 0 {
		t.Fatalf("approval must not start a run: %+v", got)
	}
	if got := s.ListIntents(); len(got) != 1 || got[0].ID != intent.ID {
		t.Fatalf("intents = %+v", got)
	}
	// An approved proposal is not decided again.
	if _, _, err := s.DecideProposal(p.ID, approved.Version, true, "", "commander"); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("second approval: err = %v, want ErrProposalConflict", err)
	}
	// Dispatch records the run and is itself one-shot.
	dispatched, err := s.MarkProposalDispatched(p.ID, approved.Version, "r_1", "operator")
	if err != nil {
		t.Fatal(err)
	}
	if dispatched.Status != model.ProposalDispatched || dispatched.RunID != "r_1" || dispatched.Version != 3 {
		t.Fatalf("dispatched = %+v", dispatched)
	}
	if _, err := s.MarkProposalDispatched(p.ID, dispatched.Version, "r_2", "operator"); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("second dispatch mark: err = %v, want ErrProposalConflict", err)
	}
}

// Approval re-runs the intent-creation validation. A reference that no longer
// exists must fail the whole decision, not leave an approved row with no intent.
func TestProposalApprovalRejectsMissingContextRefs(t *testing.T) {
	s := open(t)
	base := proposalFixture("pr_1")
	base.ContextFactIDs = []string{"f_ghost"}
	p, _, err := s.CreateProposal(base)
	if err != nil {
		t.Fatal(err)
	}
	rev := s.Revision()
	if _, _, err := s.DecideProposal(p.ID, p.Version, true, "", "commander"); !errors.Is(err, ErrFactRefMissing) {
		t.Fatalf("err = %v, want ErrFactRefMissing", err)
	}
	got, ok := s.GetProposal(p.ID)
	if !ok || got.Status != model.ProposalProposed || got.Version != 1 || got.IntentID != "" {
		t.Fatalf("proposal after refused approval = %+v", got)
	}
	if intents := s.ListIntents(); len(intents) != 0 {
		t.Fatalf("refused approval left an intent: %+v", intents)
	}
	if s.Revision() != rev {
		t.Fatal("refused approval bumped the revision")
	}
}

// Withdrawal is the proposer's undo while undecided. Someone else holding the
// same participant-shaped id is not the proposer.
func TestProposalWithdrawChecksProposerAndState(t *testing.T) {
	s := open(t)
	p, _, err := s.CreateProposal(proposalFixture("pr_1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithdrawProposal(p.ID, p.Version, "pt_in_other"); !errors.Is(err, ErrProposalForbidden) {
		t.Fatalf("foreign withdraw: err = %v, want ErrProposalForbidden", err)
	}
	if _, err := s.WithdrawProposal(p.ID, p.Version+1, "pt_in_x"); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("stale withdraw: err = %v, want ErrProposalConflict", err)
	}
	withdrawn, err := s.WithdrawProposal(p.ID, p.Version, "pt_in_x")
	if err != nil {
		t.Fatal(err)
	}
	if withdrawn.Status != model.ProposalWithdrawn || withdrawn.Version != 2 || withdrawn.DecidedBy != "pt_in_x" || withdrawn.DecidedAt == nil {
		t.Fatalf("withdrawn = %+v", withdrawn)
	}
	if _, err := s.WithdrawProposal(p.ID, withdrawn.Version, "pt_in_x"); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("second withdraw: err = %v, want ErrProposalConflict", err)
	}
	if _, _, err := s.DecideProposal(p.ID, withdrawn.Version, true, "", "commander"); !errors.Is(err, ErrProposalConflict) {
		t.Fatalf("approve withdrawn: err = %v, want ErrProposalConflict", err)
	}
}

// Reopening the ledger must show the same decision, and the returned rows must
// be detached snapshots: callers keep them after the lock is gone.
func TestProposalSurvivesRestartAndReturnsDetachedRows(t *testing.T) {
	s := open(t)
	fact, err := s.AddFact(model.Fact{Kind: "vuln", Title: "管理端可达"})
	if err != nil {
		t.Fatal(err)
	}
	base := proposalFixture("pr_1")
	base.FromFactIDs = []string{fact.ID}
	base.ContextFactIDs = []string{fact.ID}
	base.PinnedFactIDs = []string{fact.ID}
	p, _, err := s.CreateProposal(base)
	if err != nil {
		t.Fatal(err)
	}
	approved, intent, err := s.DecideProposal(p.ID, p.Version, true, "值得做", "commander")
	if err != nil {
		t.Fatal(err)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	got, ok := reopened.GetProposal(p.ID)
	if !ok || got.Status != model.ProposalApproved || got.Version != approved.Version || got.IntentID != intent.ID {
		t.Fatalf("reloaded proposal = %+v", got)
	}
	if got.DecidedBy != "commander" || got.DecidedAt == nil || got.DecisionNote != "值得做" {
		t.Fatalf("reloaded decision = %+v", got)
	}
	if _, ok := reopened.GetIntent(intent.ID); !ok {
		t.Fatal("approved intent did not survive the restart")
	}
	// Mutating the returned copy must not reach the ledger.
	got.FromFactIDs[0] = "f_mutated"
	*got.DecidedAt = got.DecidedAt.Add(time.Hour)
	fresh, _ := reopened.GetProposal(p.ID)
	if fresh.FromFactIDs[0] != fact.ID {
		t.Fatalf("returned proposal aliases the stored row: %+v", fresh.FromFactIDs)
	}
	if !fresh.DecidedAt.Equal(*approved.DecidedAt) {
		t.Fatalf("returned DecidedAt aliases the stored row: %v", fresh.DecidedAt)
	}
}

// A failed commit must leave neither half of an approval: the row-by-row
// rollback is the only thing standing between a crash and "approved with no
// intent" (the reason DecideProposal shares one flush with createIntentLocked).
func TestProposalApprovalRollsBackProposalAndIntentTogether(t *testing.T) {
	s := open(t)
	p, _, err := s.CreateProposal(proposalFixture("pr_1"))
	if err != nil {
		t.Fatal(err)
	}
	path := s.path
	if _, err := s.sqlite.db.Exec("CREATE TRIGGER fail_meta BEFORE UPDATE ON meta BEGIN SELECT RAISE(ABORT,'fixture failure'); END"); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(s.Snapshot())
	rev := s.Revision()
	if _, _, err := s.DecideProposal(p.ID, p.Version, true, "", "commander"); err == nil {
		t.Fatal("approval survived a failing commit")
	}
	after, _ := json.Marshal(s.Snapshot())
	if string(after) != string(before) || s.Revision() != rev {
		t.Fatal("failed approval changed the committed ledger")
	}
	if _, err := s.sqlite.db.Exec("DROP TRIGGER fail_meta"); err != nil {
		t.Fatal(err)
	}
	// The same decision must work once the commit path is healthy again.
	approved, intent, err := s.DecideProposal(p.ID, p.Version, true, "", "commander")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != model.ProposalApproved || approved.IntentID != intent.ID {
		t.Fatalf("approval after recovery = %+v", approved)
	}
	disk, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	if got, ok := disk.GetProposal(p.ID); !ok || got.Status != model.ProposalApproved {
		t.Fatalf("reloaded proposal = %+v", got)
	}
}
