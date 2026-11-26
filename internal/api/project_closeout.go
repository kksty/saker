package api

import (
	"context"
	"net/http"

	"github.com/kksty/saker/internal/model"
)

// The operator asks for one evidence-gated judgement without rearming workers.
// The regular runner owns prompts, context delivery, timeout and decision logs.
func (s *Server) projectCloseout(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		writeErr(w, http.StatusForbidden, "收口轮次必须由操作员请求")
		return
	}
	s.controlMu.Lock()
	if reason := s.restartRecoveryError; reason != "" {
		s.controlMu.Unlock()
		writeErr(w, http.StatusConflict, reason)
		return
	}
	if !s.paused.Load() || s.deleting.Load() || s.closing.Load() {
		s.controlMu.Unlock()
		writeErr(w, http.StatusConflict, "先暂停项目，再让指挥官收口")
		return
	}
	if s.Decider == nil || !s.Decider.Status().Enabled {
		s.controlMu.Unlock()
		writeErr(w, http.StatusConflict, "指挥官未启用，请先配置模型")
		return
	}
	open := false
	for _, g := range s.Store.Goals() {
		if !g.Closed && (!g.Met || g.ReceiptStale) {
			open = true
			break
		}
	}
	if !open {
		s.controlMu.Unlock()
		writeErr(w, http.StatusConflict, "没有需要收口的目标")
		return
	}
	for _, run := range s.Store.Runs() {
		if run.Purpose == "commander" && run.Status == model.RunRunning {
			s.controlMu.Unlock()
			writeErr(w, http.StatusConflict, "上一轮指挥官还未结束，请稍后重试")
			return
		}
	}
	if !s.closeout.CompareAndSwap(false, true) {
		s.controlMu.Unlock()
		writeErr(w, http.StatusConflict, "指挥官正在收口")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	s.closeoutCancel = cancel
	s.controlMu.Unlock()
	defer func() {
		cancel()
		s.controlMu.Lock()
		s.closeoutCancel = nil
		s.closeout.Store(false)
		s.controlMu.Unlock()
	}()
	decision := s.Decider.RunCloseout(ctx)
	if decision == nil {
		writeErr(w, http.StatusConflict, "指挥官未能开始收口，请检查模型与宿主状态")
		return
	}
	if decision.Error != "" {
		writeErr(w, http.StatusBadGateway, decision.Error)
		return
	}
	if !decision.AttentionDelivered {
		writeErr(w, http.StatusBadGateway, "收口轮次已取消，项目保持暂停")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"paused": true, "completed": s.goalsClosed(), "decision": decision})
}
