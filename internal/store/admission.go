package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/kksty/saker/internal/model"
)

// StartIntentRun commits ownership, generation, run and wait transfer together.
// A failed write leaves neither a claimed intent nor an orphan running row.
func (s *Store) StartIntentRun(id, node, owner string, run model.Run) (model.Intent, model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	brief, created, err := s.startIntentRunLocked(id, node, owner, run)
	if err != nil {
		return model.Intent{}, model.Run{}, err
	}
	if err := s.flush("intent", "run"); err != nil {
		return model.Intent{}, model.Run{}, err
	}
	return brief, created, nil
}

// StartProposalRun commits the proposal version check, the intent claim, the
// run row and the dispatch receipt in one ledger transaction. Dispatch calls it
// before launching anything: a crash or failed write can therefore leave either
// "approved, no run, no receipt" or "dispatched, run, receipt", never a run
// whose proposal still reads approved.
//
// If the proposal row already says dispatched, the recorded receipt is returned
// with created=false. The caller must not start another executor: a transport
// retry after a lost response is answered from the ledger, not by repeating the
// work.
func (s *Store) StartProposalRun(proposalID string, expectedVersion uint64, node, owner string, run model.Run) (model.Proposal, model.Intent, model.Run, bool, error) {
	proposalID = strings.TrimSpace(proposalID)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Proposals {
		p := &s.db.Proposals[i]
		if p.ID != proposalID {
			continue
		}
		if p.Status == model.ProposalDispatched {
			if p.RunID == "" || p.IntentID == "" {
				return model.Proposal{}, model.Intent{}, model.Run{}, false,
					fmt.Errorf("%w：提议 %s 已派发但缺少运行回执", ErrProposalConflict, p.ID)
			}
			recorded, ok := s.runLocked(p.RunID)
			if !ok || recorded.IntentID != p.IntentID {
				return model.Proposal{}, model.Intent{}, model.Run{}, false,
					fmt.Errorf("%w：提议 %s 的回执运行 %s 不在账本中", ErrProposalConflict, p.ID, p.RunID)
			}
			in, ok := s.intentLocked(p.IntentID)
			if !ok {
				return model.Proposal{}, model.Intent{}, model.Run{}, false,
					fmt.Errorf("%w：提议 %s 的意图 %s 不存在", ErrProposalConflict, p.ID, p.IntentID)
			}
			return detachProposal(*p), detachIntent(*in), detachRun(recorded), false, nil
		}
		if p.Status != model.ProposalApproved || p.Version != expectedVersion {
			return model.Proposal{}, model.Intent{}, model.Run{}, false,
				fmt.Errorf("%w：提议当前为 %s v%d（请求 v%d）", ErrProposalConflict, p.Status, p.Version, expectedVersion)
		}
		if p.IntentID == "" {
			return model.Proposal{}, model.Intent{}, model.Run{}, false,
				fmt.Errorf("%w：已批准的提议没有对应意图，状态不一致，请重新审批", ErrProposalConflict)
		}
		brief, created, err := s.startIntentRunLocked(p.IntentID, node, owner, run)
		if err != nil {
			return model.Proposal{}, model.Intent{}, model.Run{}, false, err
		}
		now := time.Now()
		p.Status = model.ProposalDispatched
		p.RunID = created.ID
		p.Version++
		p.UpdatedAt = now
		if err := s.flush("proposal", "intent", "run"); err != nil {
			return model.Proposal{}, model.Intent{}, model.Run{}, false, err
		}
		return detachProposal(*p), brief, detachRun(created), true, nil
	}
	return model.Proposal{}, model.Intent{}, model.Run{}, false, fmt.Errorf("%w：%s", ErrProposalNotFound, proposalID)
}

// startIntentRunLocked is the one claim implementation shared by a normal start
// and a proposal dispatch. Caller holds the write lock and owns the flush, so
// the proposal receipt can join the same transaction.
func (s *Store) startIntentRunLocked(id, node, owner string, run model.Run) (model.Intent, model.Run, error) {
	in, ok := s.intentLocked(id)
	if !ok {
		return model.Intent{}, model.Run{}, fmt.Errorf("intent %s 不存在", id)
	}
	if !handoffOwnerMatches(in, owner) {
		return model.Intent{}, model.Run{}, ErrHandoffConflict
	}
	if in.Status != model.StatusPending && in.Status != model.StatusFailed && in.Status != model.StatusReviewFailed {
		return model.Intent{}, model.Run{}, fmt.Errorf("intent %s 无法认领（状态为 %s）", id, in.Status)
	}
	now := time.Now()
	previousRun := in.RunID
	if in.Wait != nil {
		ready, results, _, reason := s.evaluateWaitLocked(in.Wait, now)
		if ready {
			in.Wait.WakePending, in.Wait.Results, in.Wait.WakeReason = true, results, reason
		}
	}
	// A satisfied wait is consumed by this claim. Its results must ride the run
	// row, not only the in-memory brief: a crash after this transaction but
	// before the executor receives the prompt otherwise loses the reason this
	// run exists. An earlier run that never confirmed receipt hands its
	// delivery forward here, so recovery redelivers the same result without
	// replaying the external action.
	var delivery *model.WaitDelivery
	if in.Wait != nil && in.Wait.WakePending {
		delivery = &model.WaitDelivery{
			Status:  model.WaitDeliveryReady,
			Wait:    detachWait(in.Wait),
			ReadyAt: &now,
		}
	} else if previous, ok := s.runLocked(previousRun); ok && previous.WaitDelivery != nil && !previous.WaitDelivery.Settled() {
		if previous.WaitDelivery.Wait != nil {
			wait := detachWait(previous.WaitDelivery.Wait)
			wait.WakePending = true
			delivery = &model.WaitDelivery{
				Status:  model.WaitDeliveryReady,
				Wait:    wait,
				ReadyAt: &now,
			}
		}
	}
	previousWait := detachWait(in.Wait)
	in.Status, in.ClaimedBy, in.Node = model.StatusRunning, node, node
	in.Hold, in.Aborted, in.HandoffOperation = false, false, ""
	in.HeldBy, in.HoldReason = "", ""
	in.Attempts++
	in.UpdatedAt = now
	run.ID, run.IntentID, run.Status = s.uniqueIDLocked("r"), id, model.RunRunning
	run.StartedAt, run.Generation = now, in.Attempts
	run.PremiseVersions = map[string]uint64{}
	for _, id := range in.FromFactIDs {
		run.PremiseVersions[id] = s.records[recordKey("fact", id)].version
	}
	run.WaitDelivery = delivery
	in.RunID = run.ID
	if in.Wait != nil {
		if in.Wait.WakePending {
			in.Wait = nil
		} else {
			in.Wait.RegisteredByRun = run.ID
		}
	}
	s.db.Runs = append(s.db.Runs, run)
	// Dispatch builds retry history from the previous holder, while the ledger
	// already points at the new run for concurrent API calls and settlement.
	brief := detachIntent(*in)
	brief.RunID = previousRun
	brief.Wait = previousWait
	if brief.Wait == nil && delivery != nil {
		// A recovered delivery is not in the intent's current wait row (the old
		// claim cleared it). The prompt builder reads it from this brief, so the
		// recovered run gets exactly the result the interrupted one was denied.
		brief.Wait = detachWait(delivery.Wait)
	}
	return brief, detachRun(run), nil
}

// MarkWaitDelivery advances one run's delivery node. Ready -> archived ->
// received is monotonic; repeating a node is a no-op so a retried archive or a
// duplicate first event cannot rewrite the observed timestamp.
func (s *Store) MarkWaitDelivery(runID, status, briefPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.Runs {
		r := &s.db.Runs[i]
		if r.ID != runID {
			continue
		}
		if r.WaitDelivery == nil {
			return fmt.Errorf("run %s 没有待交付的等待结果", runID)
		}
		d := r.WaitDelivery
		switch status {
		case model.WaitDeliveryReady:
			return nil
		case model.WaitDeliveryArchived:
			if d.Status == model.WaitDeliveryArchived || d.Status == model.WaitDeliveryReceived {
				return nil
			}
			if d.Status != model.WaitDeliveryReady {
				return fmt.Errorf("run %s 的等待结果状态为 %q，不能归档", runID, d.Status)
			}
			now := time.Now()
			d.Status = model.WaitDeliveryArchived
			d.BriefPath = briefPath
			d.ArchivedAt = &now
		case model.WaitDeliveryReceived:
			if d.Status == model.WaitDeliveryReceived {
				return nil
			}
			if d.Status != model.WaitDeliveryArchived && d.Status != model.WaitDeliveryReady {
				return fmt.Errorf("run %s 的等待结果状态为 %q，不能标记已收到", runID, d.Status)
			}
			now := time.Now()
			d.Status = model.WaitDeliveryReceived
			d.ReceivedAt = &now
		default:
			return fmt.Errorf("未知的等待结果状态 %q", status)
		}
		return s.flushNoRev("run")
	}
	return fmt.Errorf("run %s not found", runID)
}

// runLocked returns one detached run. Caller holds s.mu.
func (s *Store) runLocked(id string) (model.Run, bool) {
	for i := range s.db.Runs {
		if s.db.Runs[i].ID == id {
			return detachRun(s.db.Runs[i]), true
		}
	}
	return model.Run{}, false
}
