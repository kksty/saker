package api

import (
	"errors"
	"strings"
	"time"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
)

// hostReadTimeout bounds one host read. A local host answers in milliseconds; a
// dead one must fail with a reason instead of hanging the page.
const hostReadTimeout = 5 * time.Second

// errNoHostLink means this instance has no AOP-capable node for the request.
var errNoHostLink = errors.New("本机没有可拨号的 AOP 宿主")

// Host links are the read-only AOP/Connect endpoints of one execution node. The
// console's session-scoped reads (guardrail approvals, canonical history, native
// commands) all need the same (URL, token, agent) triple the executor uses, so
// the lookup lives here instead of being copied into every handler.

// hostForNode resolves the AOP endpoint of the node that produced a run. An
// empty node means this control plane's local node.
func (s *Server) hostForNode(node string) (base, token, agent string, ok bool) {
	tn, isTerminal := s.Exec.(executor.TerminalNode)
	if !isTerminal {
		return "", "", "", false
	}
	node = strings.TrimSpace(node)
	if node == "" {
		node = s.localNode()
	}
	if node == "" {
		return "", "", "", false
	}
	return tn.TerminalEndpoint(node)
}

// runForSession resolves a native session id to the newest run that owns it,
// preferring a live run (the same session can be continued by several runs).
// Guardrail approvals and native history are session-scoped, so this is the
// bridge from a session id to the node whose host holds it.
func (s *Server) runForSession(sessionID string) (model.Run, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || s.Store == nil {
		return model.Run{}, false
	}
	var fallback model.Run
	found := false
	for _, run := range s.Store.Runs() {
		if run.SessionID != sessionID {
			continue
		}
		if run.Status == model.RunRunning {
			return run, true
		}
		if !found || run.StartedAt.After(fallback.StartedAt) {
			fallback, found = run, true
		}
	}
	return fallback, found
}

// liveSessions is the distinct sessions of live runs, newest run first. It is
// the fan-out set for a session-less guardrail read: the inbox has no session
// parameter, and a pending approval belongs to a run that is blocked right now.
func (s *Server) liveSessions() []string {
	if s.Store == nil {
		return nil
	}
	runs := s.Store.Runs()
	out := make([]string, 0, len(runs))
	seen := make(map[string]bool, len(runs))
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		if run.Status != model.RunRunning || run.SessionID == "" || seen[run.SessionID] {
			continue
		}
		seen[run.SessionID] = true
		out = append(out, run.SessionID)
	}
	return out
}
