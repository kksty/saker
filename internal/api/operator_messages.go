package api

import (
	"encoding/json"
	"github.com/kksty/saker/internal/collab"
	"net/http"
	"strconv"
)

func (s *Server) operatorMessages(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		writeErr(w, 404, "not found")
		return
	}
	after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	if r.URL.Query().Get("after") != "" && err != nil {
		writeErr(w, 400, "invalid cursor")
		return
	}
	messages := s.Store.MessagesForBox(collab.OperatorID, after, 100, "inbox")
	next := after
	for _, m := range messages {
		if m.Seq > next {
			next = m.Seq
		}
	}
	writeJSON(w, 200, map[string]any{"messages": messages, "next": next, "more": len(messages) == 100})
}

func (s *Server) operatorMessagesSeen(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		writeErr(w, 404, "not found")
		return
	}
	var req struct {
		Through uint64 `json:"through"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeErr(w, 400, "invalid cursor")
		return
	}
	n, err := s.Store.MarkOperatorMessagesRead(req.Through)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"read": n, "through": req.Through})
}
