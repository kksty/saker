package api

// Run-scoped native-session reads and the resume recovery step.
//
// The ledger is the authority for what Saker decided; the harness session is
// the authority for what the model actually did. The controller keeps both, and
// these endpoints expose the second one read-only (canonical history and the
// native command menu) so an operator can reconstruct a run from the host's own
// event store. A failed host read returns the reason and an empty list, never
// "no events" as if the run had none.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kksty/saker/internal/aop"
	"github.com/kksty/saker/internal/aopexec"
	"github.com/kksty/saker/internal/model"
)

// runNativeSession is the shared lookup for the two session reads: the run must
// exist, the caller must be allowed to read it (same rule as the run journal),
// and the host that owns its node must be reachable.
func (s *Server) runNativeSession(w http.ResponseWriter, r *http.Request) (model.Run, bool) {
	run, ok := s.Store.GetRun(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个 run："+r.PathValue("id"))
		return model.Run{}, false
	}
	if !s.authorizeRunRead(w, r, run) {
		return model.Run{}, false
	}
	if run.SessionID == "" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "这个 run 没有原生 AOP 会话（旧运行或本地 audit 节点），没有宿主侧历史可读。",
			"code":  "no_native_session",
		})
		return model.Run{}, false
	}
	if _, _, _, ok := s.hostForNode(run.Node); !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "运行所在节点没有可拨号的 AOP 宿主，无法读取宿主侧会话数据。",
			"code":  "host_unavailable",
		})
		return model.Run{}, false
	}
	return run, true
}

// authorizeRunRead is the run-visibility rule shared by the journal and the
// native-session reads: support runs never exist for a session, and a
// participant reads only its own direction's record.
func (s *Server) authorizeRunRead(w http.ResponseWriter, r *http.Request, run model.Run) bool {
	if !harnessSession(r) {
		return true
	}
	if supportPurpose(run.Purpose) {
		writeErr(w, http.StatusNotFound, "没有这个 run："+run.ID)
		return false
	}
	if identity, participant := identityOf(r); participant && identity.IntentID != run.IntentID {
		writeErr(w, http.StatusForbidden, "只能读取自己意图的会话记录；其他意图通过事实与证据读取")
		return false
	}
	return true
}

// runHistory is GET /api/runs/{id}/history?after_cursor=&limit=. It relays the
// host's canonical ListEvents page; the events are the host's proto JSON, kept
// raw so a console renderer and an auditor see the same document.
func (s *Server) runHistory(w http.ResponseWriter, r *http.Request) {
	run, ok := s.runNativeSession(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	after := strings.TrimSpace(r.URL.Query().Get("after_cursor"))
	ctx, cancel := context.WithTimeout(r.Context(), 2*hostReadTimeout)
	defer cancel()
	base, token, _, _ := s.hostForNode(run.Node)
	page, err := aop.New(base, token).Events(ctx, run.SessionID, after, limit)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "读取宿主会话历史失败：" + s.redactSecrets(err.Error()),
			"code":  "host_read_failed",
		})
		return
	}
	events := page.Events
	if events == nil {
		events = []aop.SessionEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":      run.ID,
		"session_id":  run.SessionID,
		"events":      events,
		"next_cursor": page.NextCursor,
		"has_more":    page.NextCursor != "",
		"source":      "aop.ListEvents",
	})
}

// runCommands is GET /api/runs/{id}/commands: the native command menu of the
// run's session. It is the console command panel's data source; it never runs a
// command (a command is executed through the session's own shell/REPL).
func (s *Server) runCommands(w http.ResponseWriter, r *http.Request) {
	run, ok := s.runNativeSession(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), hostReadTimeout)
	defer cancel()
	base, token, _, _ := s.hostForNode(run.Node)
	commands, err := aop.New(base, token).Commands(ctx, run.SessionID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "读取宿主会话命令失败：" + s.redactSecrets(err.Error()),
			"code":  "host_read_failed",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":     run.ID,
		"session_id": run.SessionID,
		"commands":   commands,
		"source":     "aop.ListCommands",
	})
}

// resetGoneSession attempts the ResetSession recovery for a resume that hit a
// host which no longer knows the recorded session. It returns the replacement
// session id on success, or the reason it could not help.
//
// ResetSession itself requires the old session to exist, so the common
// NOT_FOUND case still fails here — the point is that the control plane tries
// the host's own recovery primitive once and records the outcome instead of
// jumping straight to a fresh session silently.
func (s *Server) resetGoneSession(ctx context.Context, run model.Run) (string, string) {
	if run.SessionID == "" {
		return "", "运行没有记录原生会话 id"
	}
	base, token, _, ok := s.hostForNode(run.Node)
	if !ok {
		return "", "节点没有可拨号的 AOP 宿主"
	}
	ctx, cancel := context.WithTimeout(ctx, hostReadTimeout)
	defer cancel()
	outcome, err := aop.New(base, token).ResetSession(ctx, "saker-resume-"+run.ID, run.SessionID, "", "saker: 续跑恢复")
	if err != nil {
		return "", "宿主拒绝 ResetSession：" + err.Error()
	}
	if !outcome.Accepted || outcome.Current == "" {
		code := strings.TrimSpace(outcome.Code)
		message := strings.TrimSpace(outcome.Message)
		return "", "宿主拒绝 ResetSession：" + strings.TrimSpace(code+" "+message)
	}
	workDir := filepath.Join(s.CaseRoot, run.IntentID)
	if err := aopexec.AdoptSessionForResume(workDir, outcome.Current); err != nil {
		return "", "ResetSession 已建立新会话，但写入会话标记失败：" + err.Error()
	}
	return outcome.Current, ""
}

// runMedia is GET /api/runs/{id}/media/{name}: one preserved medium (an image,
// audio clip or document a message/tool_result carried) from the run's case.
// aopexec.PreserveMedia wrote it below <case>/.tw/media, so the name is a
// generated single path element — never a user path.
func (s *Server) runMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.PathValue("name")
	if !isPlainID(id) || !isPlainMediaName(name) {
		writeErr(w, http.StatusNotFound, "媒体文件不存在："+name)
		return
	}
	run, ok := s.Store.GetRun(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个 run："+id)
		return
	}
	if !s.authorizeRunRead(w, r, run) {
		return
	}
	if run.IntentID == "" {
		writeErr(w, http.StatusNotFound, "这个 run 没有意图工作目录，也就没有媒体文件")
		return
	}
	dir := filepath.Join(s.CaseRoot, run.IntentID, filepath.FromSlash(aopexec.MediaDir))
	absDir, err := filepath.Abs(dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "解析媒体目录失败："+err.Error())
		return
	}
	abs := filepath.Join(absDir, name)
	// The generated name is already restricted, but containment is checked on
	// the resolved path too: a junction/symlink is the one way a file could
	// still point outside the case.
	if rel, err := filepath.Rel(absDir, abs); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeErr(w, http.StatusForbidden, "路径越界")
		return
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		writeErr(w, http.StatusNotFound, "媒体文件不存在："+name)
		return
	}
	if info.Size() > aopexec.MaxMediaBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("媒体文件超过单条上限 %d 字节", aopexec.MaxMediaBytes))
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		writeErr(w, http.StatusNotFound, "媒体文件不可读："+name)
		return
	}
	defer f.Close()
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Content-Disposition", `inline; filename="`+name+`"`)
	if ct := mediaContentType(name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if isSandboxedDocument(name) {
		// A preserved SVG/HTML is attacker-influenced data: never let it run
		// with the console's origin when the operator previews it.
		w.Header().Set("Content-Security-Policy", "sandbox")
	}
	// ServeContent gives range requests for audio/video scrubbing without a
	// second implementation.
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// isPlainMediaName accepts exactly the file-name shape PreserveMedia generates:
// one path element of letters, digits, dot, dash and underscore. It rejects
// separators, dot-segments and Windows device names by construction.
func isPlainMediaName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// mediaContentType maps the preserved extension to a preview type. It uses a
// fixed table (not the OS registry) so two machines answer the same and an
// unknown extension is a download, not a guessed document.
func mediaContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".tiff":
		return "image/tiff"
	case ".svg":
		return "image/svg+xml"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	case ".m4a":
		return "audio/mp4"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".pdf":
		return "application/pdf"
	case ".json":
		return "application/json"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	}
	return "application/octet-stream"
}

func isSandboxedDocument(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".svg", ".html", ".xml":
		return true
	}
	return false
}
