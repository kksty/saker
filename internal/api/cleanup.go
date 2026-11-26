package api

import (
	"context"
	"github.com/kksty/saker/internal/aopexec"
	"github.com/kksty/saker/internal/model"
	"path/filepath"
	"time"
)

// cleanupNotice is the one evidence-wrap-up instruction. Two bounded windows
// deliver it: the timeout budget's tail (startCleanupWindow) and a process stop
// (DrainRunning). Keeping it a package constant — and delivering it through the
// same helper — is what stops the two paths from drifting into two different
// promises. It deliberately does not claim the turn is over or that anything
// was found: it asks for the observations that already exist to be written down.
const cleanupNotice = "运行机制通知：本轮已进入有界证据收尾阶段（超时预算或停机窗口临近）。停止新增探测，先读工具结果里已落盘的命令输出原件（结果给出输出文件路径时按路径读取）；保留已取得的工具原件与证据，把能按原件复核的观察按事实交接契约提交并更新 worker-note，标明仍未完成或结果未知的部分，然后结束本轮。推测和残余日志摘要不自动成为事实；授权与验收口径不变。"

// cleanupReserve is the tail of the run budget reserved for writing evidence
// down. It scales with the budget: a fixed 90s was too short for a long drill
// (2.5% of 60m) and too long for a short one. clamp(remaining/10, 60s, 300s),
// and never more than half the budget so even a short run keeps a working
// phase. A spent or negative budget reserves nothing (returns 0), so callers
// never depend on a downstream short-circuit to keep a negative value away.
func cleanupReserve(remaining time.Duration) time.Duration {
	if remaining <= 0 {
		return 0
	}
	reserve := remaining / 10
	if reserve < 60*time.Second {
		reserve = 60 * time.Second
	}
	if reserve > 300*time.Second {
		reserve = 300 * time.Second
	}
	if reserve > remaining/2 {
		reserve = remaining / 2
	}
	return reserve
}

// startCleanupWindow uses the harness's existing active-session inbox. It
// reserves part of the already configured timeout and never extends a pause or
// hard deadline.
func (s *Server) startCleanupWindow(ctx context.Context, runID, intentID, workDir string) func() {
	deadline, ok := ctx.Deadline()
	if !ok || s.Timeout <= 0 || (s.Say == nil && s.SayEvent == nil) {
		return func() {}
	}
	remaining := time.Until(deadline)
	reserve := cleanupReserve(remaining)
	if reserve <= 0 {
		return func() {}
	}
	stopCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(remaining - reserve)
		defer timer.Stop()
		select {
		case <-stopCtx.Done():
			return
		case <-timer.C:
		}
		if stopCtx.Err() != nil {
			return
		}
		_, _ = s.deliverCleanupNotice(stopCtx, runID, intentID, workDir, time.Until(deadline))
	}()
	return func() { stop(); <-done }
}

// deliverCleanupNotice is the single delivery path for cleanupNotice: it
// resolves the direction and its live AOP session, claims the run's cleanup
// phase, sends the interrupt through SayEvent/Say, records the outcome and
// journals the receipt. Every step is conditional on the phase still being
// empty, so a second window (or a shutdown drain after a timeout window) never
// delivers the notice twice and never writes a second "cleanup" event.
//
// A missing intent/session is not an error: the run may have been replaced or
// already lost its harness. The caller keeps its own deadline and cancels; the
// helper simply reports that nothing could be delivered.
func (s *Server) deliverCleanupNotice(ctx context.Context, runID, intentID, workDir string, remaining time.Duration) (string, error) {
	if s.Say == nil && s.SayEvent == nil {
		return "", nil
	}
	if ctx.Err() != nil {
		return "", nil
	}
	in, ok := s.Store.GetIntent(intentID)
	if !ok || in.RunID != runID {
		return "", nil
	}
	sess, ok := aopexec.ReadSession(filepath.Clean(workDir))
	if !ok {
		return "", nil
	}
	claimed := false
	if _, err := s.Store.UpdateRun(runID, func(r *model.Run) {
		if r.CleanupPhase != "" {
			return
		}
		r.CleanupPhase = "requested"
		claimed = true
	}); err != nil {
		return "", err
	}
	if !claimed {
		return "", nil
	}
	var receipt string
	var err error
	if s.SayEvent != nil {
		receipt, err = s.SayEvent(ctx, in, sess, cleanupNotice, true, "cleanup:"+runID)
	} else {
		receipt, err = s.Say(ctx, in, sess, cleanupNotice, true)
	}
	phase := "accepted"
	detail := ""
	if err != nil {
		phase = "delivery_failed"
		detail = err.Error()
	}
	_, _ = s.Store.UpdateRun(runID, func(r *model.Run) { r.CleanupPhase = phase })
	s.appendEventLogged(runID, "cleanup", map[string]any{"kind": "cleanup", "at": time.Now(), "phase": phase, "receipt": receipt, "error": s.redactSecrets(detail), "remaining_ms": remaining.Milliseconds()})
	return phase, err
}
