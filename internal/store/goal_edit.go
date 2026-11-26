package store

import (
	"fmt"
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"strings"
	"time"
)

// EditGoal changes the definition and withdraws its old completion receipt.
// Historical versions retain the prior definition and its evidence stamp.
func (s *Store) EditGoal(id, statement, start, instructionRef, runID string) (model.Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return model.Goal{}, fmt.Errorf("statement 不能为空")
	}
	for i := range s.db.Goals {
		g := &s.db.Goals[i]
		if g.ID != id {
			continue
		}
		if g.Closed {
			return model.Goal{}, fmt.Errorf("目标已关闭")
		}
		if g.Statement == statement && g.Start == strings.TrimSpace(start) && g.InstructionRef == instructionRef {
			return detachGoal(*g), nil
		}
		if err := s.bindGoalInstructionLocked(instructionRef, runID); err != nil {
			return model.Goal{}, err
		}
		g.Statement, g.Start, g.InstructionRef = statement, strings.TrimSpace(start), instructionRef
		g.CreatedByRun = runID
		g.Met = false
		g.EvidenceID = ""
		g.Note = ""
		g.From = nil
		g.FromClaimVersions = nil
		g.CompletedAt = nil
		g.CompletionPolicy = ""
		g.ReceiptStale = false
		g.ReceiptChanged = nil
		if err := s.flush("goal", "message"); err != nil {
			return model.Goal{}, err
		}
		return detachGoal(*g), nil
	}
	return model.Goal{}, fmt.Errorf("goal %s not found", id)
}

func (s *Store) CloseGoal(id, reason, instructionRef, runID string) (model.Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return model.Goal{}, fmt.Errorf("关闭目标必须写 reason")
	}
	for i := range s.db.Goals {
		g := &s.db.Goals[i]
		if g.ID != id {
			continue
		}
		if g.Closed {
			if g.ClosedReason == reason && g.InstructionRef == instructionRef {
				return detachGoal(*g), nil
			}
			return model.Goal{}, fmt.Errorf("目标已经关闭")
		}
		now := time.Now()
		if err := s.bindGoalInstructionLocked(instructionRef, runID); err != nil {
			return model.Goal{}, err
		}
		g.Closed = true
		g.ClosedAt = &now
		g.ClosedReason = reason
		g.InstructionRef = instructionRef
		g.CreatedByRun = runID
		if err := s.flush("goal", "message"); err != nil {
			return model.Goal{}, err
		}
		return detachGoal(*g), nil
	}
	return model.Goal{}, fmt.Errorf("goal %s not found", id)
}

func (s *Store) bindGoalInstructionLocked(ref, runID string) error {
	if ref == "" && runID == "" {
		return nil
	}
	live := false
	for _, r := range s.db.Runs {
		if r.ID == runID && r.Purpose == "commander" && r.Status == model.RunRunning {
			live = true
			break
		}
	}
	if !live {
		return fmt.Errorf("目标变更需要当前指挥官轮次")
	}
	for i := range s.db.Messages {
		m := &s.db.Messages[i]
		if m.ID == ref && m.FromParticipantID == collab.OperatorID && m.ToParticipantID == collab.CommanderID && strings.TrimSpace(m.Text) != "" {
			m.GoalInstruction = true
			m.Durable = true
			return nil
		}
	}
	return fmt.Errorf("目标变更指令不存在或并非操作员消息")
}
