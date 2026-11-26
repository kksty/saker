package api

import (
	"time"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// guardInfo summarizes watchdog observations for the operator console.
type guardInfo struct {
	IntervalSeconds    int             `json:"interval_seconds"`
	IdleTimeoutSeconds int             `json:"idle_timeout_seconds,omitempty"`
	StartedAt          *time.Time      `json:"started_at,omitempty"`
	CompletedAt        *time.Time      `json:"completed_at,omitempty"`
	InProgress         bool            `json:"in_progress"`
	GuardCanceled      int             `json:"guard_canceled"`
	AutoRedispatched   int             `json:"auto_redispatched"`
	Recent             []guardRun      `json:"recent,omitempty"`
	Activity           []guardActivity `json:"activity,omitempty"`
}

type guardRun struct {
	RunID          string                       `json:"run_id"`
	SuccessorRunID string                       `json:"successor_run_id,omitempty"`
	Reason         string                       `json:"reason,omitempty"`
	Disposition    *model.RedispatchDisposition `json:"disposition,omitempty"`
}

func (g *guardInfo) observeRuns(runs []model.Run) {
	successors := make(map[string][]string)
	for _, r := range runs {
		if r.AutoRedispatchedFrom != "" {
			successors[r.AutoRedispatchedFrom] = append(successors[r.AutoRedispatchedFrom], r.ID)
			g.AutoRedispatched++
		}
	}
	// Store append order makes the last entries the most recently started.
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		canceled := r.Status == model.RunCanceled && r.CancelCause == model.CancelCauseWatchdog
		if canceled {
			g.GuardCanceled++
		}
		if len(g.Recent) >= 3 {
			continue
		}
		for _, id := range successors[r.ID] {
			g.Recent = append(g.Recent, guardRun{RunID: r.ID, SuccessorRunID: id, Reason: r.ExitReason})
			if len(g.Recent) >= 3 {
				break
			}
		}
		if len(successors[r.ID]) == 0 && (canceled || r.RedispatchDisposition != nil) {
			g.Recent = append(g.Recent, guardRun{RunID: r.ID, Reason: r.ExitReason, Disposition: r.RedispatchDisposition})
		}
	}
}

type guardActivity struct {
	IntentID   string `json:"intent_id"`
	HostOnline *bool  `json:"host_online,omitempty"`
	TurnEnded  bool   `json:"turn_ended,omitempty"`
	OpenTools  int    `json:"open_tools,omitempty"`
	Facts      int    `json:"facts"`
}

func (s *Server) guardActivity(db store.DB, running []board.Item) []guardActivity {
	if len(running) == 0 {
		return nil
	}
	nodes := make(map[string]*bool)
	if aware, ok := s.Exec.(executor.NodeAware); ok {
		for _, node := range aware.Nodes() {
			nodes[node.ID] = node.Online
		}
	}
	if s.LocalHostStatus != nil {
		online := s.LocalHostStatus().Online
		nodes[s.localNode()] = &online
	}
	runs := make(map[string]model.Run, len(running))
	for _, r := range db.Runs {
		if r.Status == model.RunRunning {
			runs[r.ID] = r
		}
	}
	facts := make(map[string]int)
	for _, f := range db.Facts {
		facts[f.IntentID]++
	}
	out := make([]guardActivity, 0, len(running))
	for _, in := range running {
		m := guardActivity{IntentID: in.ID, Facts: facts[in.ID]}
		if in.Pulse != nil {
			r := runs[in.Pulse.Run]
			m.HostOnline = nodes[r.Node]
			m.TurnEnded = r.HarnessTurnEnded
			m.OpenTools = len(r.OpenToolCalls)
		}
		out = append(out, m)
	}
	return out
}
