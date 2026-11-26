package api

// Fact withdrawal: an authoring direction or the operator says "this record
// should not stand". It is deliberately not a delete and not a review verdict.
//
// The ledger keeps the row, its evidence and every earlier version: a later
// reader can always see what was claimed, who retracted it and why. What
// changes is usability — a withdrawn assertion cannot be a premise, a goal
// receipt, a handoff reference or a report finding (model.Fact.Unusable), and
// the work surfaces stop presenting it as project knowledge.
//
// Authority is authorship: a direction may retract its own assertion while it
// is still unreviewed; the operator may retract any assertion. The commander
// may not retract someone else's fact — it organizes work and does not own the
// evidence — and a reviewed outcome (verified or rejected) outranks its author,
// so only the operator may withdraw it.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kksty/saker/internal/collab"
)

func (s *Server) withdrawFact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f, ok := s.Store.FactByID(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "fact not found")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		writeErr(w, http.StatusBadRequest, "撤回必须写理由：说明为什么这条记录不应保留")
		return
	}
	actor, runID := collab.OperatorID, ""
	c := s.caller(r)
	if c.agent {
		if c.participant == collab.CommanderID {
			writeErr(w, http.StatusForbidden, "指挥官不能撤回事实：证据由执行者与操作员负责")
			return
		}
		if f.IntentID == "" || c.participant != collab.ParticipantID(f.IntentID) {
			writeErr(w, http.StatusForbidden, "只能撤回自己意图声明的事实")
			return
		}
		if f.Status == "verified" || f.Status == "rejected" {
			writeErr(w, http.StatusConflict, "已核验或已否决的事实只能由操作员撤回：复核结论不由作者单方抹去")
			return
		}
		actor, runID = c.participant, c.runID
	}
	out, err := s.Store.WithdrawFact(id, actor, runID, reason)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.routeAndDeliver()
	if c.agent {
		s.recordActionFor(c.runID, "withdraw", id, reason)
	} else {
		s.recordAction("withdraw", id, reason)
	}
	writeJSON(w, http.StatusOK, map[string]any{"fact": out})
}
