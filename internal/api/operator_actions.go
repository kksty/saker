package api

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	modelpkg "github.com/kksty/saker/internal/model"
)

// operatorActionsFile is the append-only record of the operator's own goal
// decisions. Nothing else records them durably: a pause keeps only the current
// state in project.json, settings keep the config, an appended goal keeps only
// the goal row, and the in-memory action list reaches decisions.jsonl only
// through the next commander round — an operator write has no round, and a
// reopened goal no longer carries who stamped it. The operator is the final
// arbiter and must not be the one actor with no trace.
//
// The scope is deliberately two actions: goal_met and goal_reopen. Every other
// operator write already leaves a durable row of its own (document revisions,
// goal rows, instructions, messages); this file exists for the decision that a
// reopen erases.
const operatorActionsFile = "operator-actions.jsonl"

// operatorAction is one line of that file.
//
// Action is goal_met or goal_reopen. By is "operator" or the commander run that
// withdrew a stamp. The stamped_* fields describe the stamp being withdrawn —
// they are the only copy left once ReopenGoal clears the goal row.
type operatorAction struct {
	At          time.Time `json:"at"`
	Action      string    `json:"action"`
	GoalID      string    `json:"goal_id"`
	By          string    `json:"by"`
	Note        string    `json:"note,omitempty"`
	EvidenceID  string    `json:"evidence_id,omitempty"`
	From        []string  `json:"from,omitempty"`
	MetByRun    string    `json:"met_by_run_id,omitempty"`
	StampedBy   string    `json:"stamped_by,omitempty"`
	StampedAt   string    `json:"stamped_at,omitempty"`
	StampedNote string    `json:"stamped_note,omitempty"`
}

// operatorActionMu serialises appends across the server's goroutines. One
// process owns a project directory, so this is enough; a partially written line
// would make the audit unreadable.
var operatorActionMu sync.Mutex

// recordOperatorAction appends one line and syncs it, returning the write error
// to the caller: an audit that can fail silently is not an audit. A server with
// no DataDir (a bare test server) is refused rather than skipped — production
// always runs with the project directory, and skipping would hide the failure
// exactly where it matters.
func (s *Server) recordOperatorAction(a operatorAction) error {
	if s == nil || strings.TrimSpace(s.DataDir) == "" {
		return errors.New("没有项目数据目录：操作员审计无法落盘")
	}
	a.At = time.Now()
	line, err := json.Marshal(a)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	operatorActionMu.Lock()
	defer operatorActionMu.Unlock()
	if err := os.MkdirAll(s.DataDir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.DataDir, operatorActionsFile),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// stampedGoalAction is the goal_met line for a stamp just applied.
func stampedGoalAction(g modelpkg.Goal) operatorAction {
	return operatorAction{
		Action:     "goal_met",
		GoalID:     g.ID,
		By:         "operator",
		Note:       g.Note,
		EvidenceID: g.EvidenceID,
		From:       g.From,
		MetByRun:   g.MetByRun,
	}
}
