package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

// Document kinds. The set is closed: a prompt is built from exactly these three,
// and a fourth kind would silently never be injected.
const (
	DocumentScope       = "scope"
	DocumentGoal        = "goal"
	DocumentConstraints = "constraints"
)

var ErrDocumentConflict = fmt.Errorf("文档已被改过")

func ValidDocumentKind(kind string) bool {
	switch kind {
	case DocumentScope, DocumentGoal, DocumentConstraints:
		return true
	}
	return false
}

func detachDocument(d model.Document) model.Document { return d }

// Documents returns the stored revisions in a stable order (constraints, goal,
// scope) so a caller can render them without sorting.
func (s *Store) Documents() []model.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Document, 0, len(s.db.Documents))
	for _, kind := range []string{DocumentConstraints, DocumentGoal, DocumentScope} {
		for _, d := range s.db.Documents {
			if d.Kind == kind {
				out = append(out, detachDocument(d))
			}
		}
	}
	return out
}

func (s *Store) Document(kind string) (model.Document, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.db.Documents {
		if d.Kind == kind {
			return detachDocument(d), true
		}
	}
	return model.Document{}, false
}

// DocumentRevision reads one stored revision from the ledger history. The
// history is the audit trail: every save appends a full snapshot, so an old
// version can be read back after a restart without a second table.
func (s *Store) DocumentRevision(kind string, revision uint64) (model.Document, bool) {
	for _, c := range s.History("document", kind) {
		var d model.Document
		if err := json.Unmarshal(c.Value, &d); err != nil {
			continue
		}
		if d.Revision == revision {
			return d, true
		}
	}
	return model.Document{}, false
}

func (s *Store) documentRevisionLocked(kind string) (model.Document, bool) {
	for i := range s.db.Documents {
		if s.db.Documents[i].Kind == kind {
			return s.db.Documents[i], true
		}
	}
	return model.Document{}, false
}

func (s *Store) putDocumentLocked(next model.Document) {
	for i := range s.db.Documents {
		if s.db.Documents[i].Kind == next.Kind {
			s.db.Documents[i] = next
			return
		}
	}
	s.db.Documents = append(s.db.Documents, next)
}

// SaveDocument writes the next revision of one document. expected is the
// revision the caller read: a mismatch is a conflict, not a silent overwrite —
// two consoles must not race each other into losing an edit.
func (s *Store) SaveDocument(expected uint64, kind, text, by string) (model.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidDocumentKind(kind) {
		return model.Document{}, fmt.Errorf("未知文档：%s", kind)
	}
	if strings.TrimSpace(by) == "" {
		return model.Document{}, fmt.Errorf("文档保存必须记录修改人")
	}
	current, found := s.documentRevisionLocked(kind)
	if found && current.Revision != expected {
		return current, ErrDocumentConflict
	}
	if !found && expected != 0 {
		return model.Document{}, ErrDocumentConflict
	}
	if found && current.Text == text {
		return current, nil
	}
	next := model.Document{
		Kind: kind, Revision: expected + 1, Text: text,
		UpdatedAt: time.Now(), UpdatedBy: by,
	}
	s.putDocumentLocked(next)
	if err := s.flushNoRev("document"); err != nil {
		return model.Document{}, err
	}
	return next, nil
}

// CurrentGoal returns the earliest open goal and its revision from one ledger read.
func (s *Store) CurrentGoal() (model.Goal, uint64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	current := s.currentGoalLocked()
	if current == nil {
		return model.Goal{}, 0, false
	}
	revision := s.records[recordKey("goal", current.ID)].version
	return detachGoal(*current), revision, true
}

func (s *Store) currentGoalLocked() *model.Goal {
	var current *model.Goal
	for i := range s.db.Goals {
		g := &s.db.Goals[i]
		if !g.Closed && (current == nil || g.CreatedAt.Before(current.CreatedAt)) {
			current = g
		}
	}
	return current
}

// GoalRevision is the revision of the goal document: it is the goal row's own
// version, not a second counter. The goal statement is the only copy, so its
// ledger version is what a console sends back to prove it read the current text.
func (s *Store) GoalRevision(id string) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r, ok := s.records[recordKey("goal", id)]; ok {
		return r.version
	}
	for _, g := range s.db.Goals {
		if g.ID == id {
			return 1
		}
	}
	return 0
}

// GoalDocumentHistory lists the goal statement's revisions from the goal row's
// own version journal. The statement is the only copy, so this is the audit
// trail — not a second document row.
func (s *Store) GoalDocumentHistory(goalID string) []model.Document {
	out := []model.Document{}
	for _, c := range s.History("goal", goalID) {
		var g model.Goal
		if err := json.Unmarshal(c.Value, &g); err != nil {
			continue
		}
		out = append(out, model.Document{
			Kind: DocumentGoal, Revision: c.Version, Text: g.Statement,
			UpdatedAt: c.At, UpdatedBy: g.UpdatedBy, GoalID: goalID,
		})
	}
	return out
}

// SaveGoalDocument writes the goal document: the statement (and optionally the
// start-point and the operator instruction it cites), compared against the goal
// row's own revision. Doing it in two calls would leave a window where the
// statement changed but the revision did not, and the console would then
// overwrite a newer edit without a conflict.
func (s *Store) SaveGoalDocument(expected uint64, goalID, statement, start, instructionRef, runID, by string) (model.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveGoalDocumentLocked(expected, goalID, statement, start, instructionRef, runID, by)
}

// SaveCurrentGoalStatement binds the draft to the current goal under the same
// lock as the revision check. The document editor changes only the statement.
func (s *Store) SaveCurrentGoalStatement(expected uint64, goalID, statement, by string) (model.Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	goal := s.currentGoalLocked()
	if goal == nil || goal.ID != goalID {
		return model.Document{}, ErrDocumentConflict
	}
	if strings.TrimSpace(statement) == goal.Statement {
		return s.saveGoalDocumentLocked(expected, goalID, statement, goal.Start, goal.InstructionRef, goal.CreatedByRun, by)
	}
	return s.saveGoalDocumentLocked(expected, goalID, statement, goal.Start, "", "", by)
}

func (s *Store) saveGoalDocumentLocked(expected uint64, goalID, statement, start, instructionRef, runID, by string) (model.Document, error) {
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return model.Document{}, fmt.Errorf("目标文档不能为空")
	}
	if strings.TrimSpace(by) == "" {
		return model.Document{}, fmt.Errorf("文档保存必须记录修改人")
	}
	var current uint64
	if r, ok := s.records[recordKey("goal", goalID)]; ok {
		current = r.version
	}
	if current != expected {
		return model.Document{}, ErrDocumentConflict
	}
	done := false
	changed := false
	var updated model.Goal
	for i := range s.db.Goals {
		g := &s.db.Goals[i]
		if g.ID != goalID {
			continue
		}
		if g.Closed {
			return model.Document{}, fmt.Errorf("目标已关闭")
		}
		if g.Statement != statement || strings.TrimSpace(start) != g.Start || g.InstructionRef != instructionRef {
			if err := s.bindGoalInstructionLocked(instructionRef, runID); err != nil {
				return model.Document{}, err
			}
			g.Statement, g.Start, g.InstructionRef = statement, strings.TrimSpace(start), instructionRef
			g.CreatedByRun = runID
			// Who wrote these words, not only which run supplied the instruction:
			// an operator edit clears the run attribution the same way it clears
			// CreatedByRun, so the author of the words is never guessed later.
			g.UpdatedBy = by
			changed = true
			// Editing the definition withdraws the old completion receipt, exactly
			// as EditGoal does: a stamp issued for other words is not evidence for
			// these.
			g.Met, g.EvidenceID, g.Note = false, "", ""
			g.From, g.FromClaimVersions, g.CompletedAt = nil, nil, nil
			g.CompletionPolicy, g.ReceiptStale, g.ReceiptChanged = "", false, nil
		}
		updated = detachGoal(*g)
		done = true
		break
	}
	if !done {
		return model.Document{}, fmt.Errorf("goal %s not found", goalID)
	}
	if !changed {
		// Nothing was written, so the row's version did not move either. Returning
		// current+1 here would hand the console a revision that does not exist.
		return model.Document{
			Kind: DocumentGoal, Revision: current, Text: updated.Statement, GoalID: goalID,
		}, nil
	}
	if err := s.flush("goal", "message"); err != nil {
		return model.Document{}, err
	}
	return model.Document{
		Kind: DocumentGoal, Revision: current + 1, Text: updated.Statement,
		UpdatedAt: time.Now(), UpdatedBy: by, GoalID: goalID,
	}, nil
}
