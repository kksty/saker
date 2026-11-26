package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// A source reference proves who supplied the instruction, not that every
// interpretation of its prose is correct. The operator can inspect both the
// message and the durable goal change. Only a live commander may act for them.
func (s *Server) authorizeGoalDefinition(w http.ResponseWriter, r *http.Request, messageID string) (string, string, bool) {
	if !harnessSession(r) {
		return "", "", true
	}
	c := s.caller(r)
	run, live := s.Store.GetRun(c.runID)
	msg, exists := s.Store.MessageByID(strings.TrimSpace(messageID))
	if !c.agent || c.participant != collab.CommanderID || !live || run.Purpose != "commander" || run.Status != model.RunRunning || !exists || msg.FromParticipantID != collab.OperatorID || msg.ToParticipantID != collab.CommanderID || strings.TrimSpace(msg.Text) == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "目标变更必须由操作员执行，或由当前指挥官引用操作员发给指挥官的明确指令 operator_message_id", "code": "goal_operator_only"})
		return "", "", false
	}
	return msg.ID, c.runID, true
}

func (s *Server) goalByID(id string) model.Goal {
	for _, g := range s.Store.Goals() {
		if g.ID == id {
			return g
		}
	}
	return model.Goal{ID: id}
}

// authorizeGoalWithdrawal decides who may withdraw a completion stamp.
//
// The operator may always; a commander session may only withdraw a stamp a
// commander run made. A stamp with an empty MetByRun is the operator's manual
// pin — or a goal stamped before that field existed — and a session erasing it
// would silently delete the operator's judgement and its one-line reason, with
// no round log to point at. The session path still requires a live commander
// round, the same identity gate authorizeGoalDefinition uses, so a stale token
// cannot erase a commander's own stamp either.
func (s *Server) authorizeGoalWithdrawal(w http.ResponseWriter, r *http.Request, g model.Goal) bool {
	if !harnessSession(r) {
		return true
	}
	if strings.TrimSpace(g.MetByRun) == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "这一版目标由操作员手工盖章：撤销必须由操作员执行",
			"code":  "goal_operator_only",
		})
		return false
	}
	c := s.caller(r)
	run, live := s.Store.GetRun(c.runID)
	if !c.agent || c.participant != collab.CommanderID || !live || run.Purpose != "commander" || run.Status != model.RunRunning {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "撤销盖章只能由活着的指挥官轮次执行",
			"code":  "goal_operator_only",
		})
		return false
	}
	return true
}

// updateGoal edits a goal definition. It requires the revision the caller read:
// the goal statement is the goal document, and two editors (this dialog and the
// documents page) must not overwrite each other silently. The revision check and
// the edit happen in one store transaction.
func (s *Server) updateGoal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Statement         string  `json:"statement"`
		Start             string  `json:"start"`
		OperatorMessageID string  `json:"operator_message_id"`
		Revision          *uint64 `json:"revision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	ref, run, ok := s.authorizeGoalDefinition(w, r, req.OperatorMessageID)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if req.Revision == nil {
		current := s.documentsView()[store.DocumentGoal]
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "目标编辑必须带修订号：刷新看当前版再改",
			"code":    "document_conflict",
			"current": map[string]any{"revision": current.Revision, "updated_at": current.UpdatedAt},
		})
		return
	}
	if _, err := s.Store.SaveGoalDocument(*req.Revision, id, req.Statement, req.Start, ref, run, documentAuthor(r)); err != nil {
		if errors.Is(err, store.ErrDocumentConflict) {
			current := s.documentsView()[store.DocumentGoal]
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   "目标已被改过：刷新看当前版再改",
				"code":    "document_conflict",
				"current": map[string]any{"revision": current.Revision, "updated_at": current.UpdatedAt},
			})
			return
		}
		writeErr(w, 409, err.Error())
		return
	}
	s.recordAction("goal_update", id, req.Statement)
	writeJSON(w, 200, s.goalByID(id))
}

func (s *Server) closeGoal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason            string `json:"reason"`
		OperatorMessageID string `json:"operator_message_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	ref, run, ok := s.authorizeGoalDefinition(w, r, req.OperatorMessageID)
	if !ok {
		return
	}
	g, err := s.Store.CloseGoal(r.PathValue("id"), req.Reason, ref, run)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	s.recordAction("goal_close", g.ID, g.ClosedReason)
	writeJSON(w, 200, g)
}
