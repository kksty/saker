package api

import (
	"context"
	"encoding/json"
	"github.com/kksty/saker/internal/testutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/saker/internal/aopexec"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// sessionExec records dispatched requests without doing any work. The native
// session tests assert the retry decision (resume vs fresh) from it, and its
// endpoint is the fake host's AOP/Connect base URL.
type sessionExec struct {
	mu       sync.Mutex
	reqs     []executor.Request
	runErr   error
	endpoint string
}

func (e *sessionExec) Name() string { return "session-fake" }
func (e *sessionExec) Run(_ context.Context, req executor.Request, _ func(executor.Event)) (executor.Result, error) {
	e.mu.Lock()
	e.reqs = append(e.reqs, req)
	e.mu.Unlock()
	return executor.Result{ExitCode: 0}, e.runErr
}
func (e *sessionExec) requests() []executor.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]executor.Request{}, e.reqs...)
}
func (e *sessionExec) TerminalEndpoint(string) (string, string, string, bool) {
	if e.endpoint == "" {
		return "", "", "", false
	}
	return e.endpoint, "hub-token", "local", true
}

type nativeFixture struct {
	server *Server
	dir    string
	intent model.Intent
	run    model.Run
	exec   *sessionExec
}

func newNativeFixture(t *testing.T, endpoint string) *nativeFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &sessionExec{endpoint: endpoint}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	f := &nativeFixture{server: s, dir: dir, exec: ex}
	in, err := st.CreateIntent(model.Intent{Title: "会话读取"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: in.ID, Node: "local", Status: model.RunRunning, SessionID: "sess-1", StartedAt: time.Now(), Purpose: "executor"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.CaseRoot, in.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	f.intent, f.run = in, run
	return f
}

func (f *nativeFixture) call(t *testing.T, handler func(http.ResponseWriter, *http.Request), method, target string, body string, values map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	for key, value := range values {
		req.SetPathValue(key, value)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// nativeSessionRPC serves the SessionService Connect JSON methods the read
// handlers use. The recorded request documents pin the proto JSON field names.
type nativeSessionRPC struct {
	mu            sync.Mutex
	listEvents    map[string]any
	listCommands  map[string]any
	resetRequests []map[string]any
	resetAccepted bool
}

func (h *nativeSessionRPC) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/ListEvents"):
			h.mu.Lock()
			h.listEvents = body
			h.mu.Unlock()
			_, _ = w.Write([]byte(`{"events":[{"cursor":"c1","event":{"id":"ev-1","sessionId":"sess-1","payload":{"message":{"role":"assistant"}}}}],"nextCursor":"c1"}`))
		case strings.HasSuffix(r.URL.Path, "/ListCommands"):
			h.mu.Lock()
			h.listCommands = body
			h.mu.Unlock()
			_, _ = w.Write([]byte(`{"commands":[{"name":"help","usage":"/help","description":"菜单"},{"name":"clear"}]}`))
		case strings.HasSuffix(r.URL.Path, "/ResetSession"):
			h.mu.Lock()
			h.resetRequests = append(h.resetRequests, body)
			accepted := h.resetAccepted
			h.mu.Unlock()
			if !accepted {
				_, _ = w.Write([]byte(`{"requestId":"saker","rejected":{"code":"NOT_FOUND","message":"session not found"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"requestId":"saker","accepted":{"previous":{"id":"sess-gone"},"current":{"session":{"id":"sess-new"}}}}`))
		default:
			http.NotFound(w, r)
		}
	})
}

func TestRunHistoryRelaysCanonicalEvents(t *testing.T) {
	rpc := &nativeSessionRPC{}
	srv := testutil.NewServer(t, rpc.handler(t))
	defer srv.Close()
	f := newNativeFixture(t, srv.URL)
	f.run, _ = f.server.Store.UpdateRun(f.run.ID, func(r *model.Run) { r.SessionID = "sess-1" })

	rec := f.call(t, f.server.runHistory, http.MethodGet, "/api/runs/"+f.run.ID+"/history?after_cursor=c0&limit=25", "", map[string]string{"id": f.run.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("history = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Events []struct {
			Cursor string          `json:"cursor"`
			Event  json.RawMessage `json:"event"`
		} `json:"events"`
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
		Source     string `json:"source"`
		SessionID  string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 1 || out.Events[0].Cursor != "c1" || !strings.Contains(string(out.Events[0].Event), "ev-1") {
		t.Fatalf("history = %s", rec.Body.String())
	}
	if out.NextCursor != "c1" || !out.HasMore || out.Source != "aop.ListEvents" || out.SessionID != "sess-1" {
		t.Fatalf("history meta = %s", rec.Body.String())
	}
	if rpc.listEvents["sessionId"] != "sess-1" || rpc.listEvents["afterCursor"] != "c0" || rpc.listEvents["limit"] != float64(25) {
		t.Fatalf("ListEvents request = %v", rpc.listEvents)
	}
}

func TestRunHistoryExplainsHostFailure(t *testing.T) {
	srv := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"internal","message":"store unavailable"}`))
	}))
	defer srv.Close()
	f := newNativeFixture(t, srv.URL)
	rec := f.call(t, f.server.runHistory, http.MethodGet, "/api/runs/"+f.run.ID+"/history", "", map[string]string{"id": f.run.ID})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("host failure = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !strings.Contains(out.Error, "读取宿主会话历史失败") || !strings.Contains(out.Error, "store unavailable") {
		t.Fatalf("failure reason = %s", rec.Body.String())
	}
}

func TestRunHistoryRequiresNativeSession(t *testing.T) {
	f := newNativeFixture(t, "")
	f.run, _ = f.server.Store.UpdateRun(f.run.ID, func(r *model.Run) { r.SessionID = "" })
	rec := f.call(t, f.server.runHistory, http.MethodGet, "/api/runs/"+f.run.ID+"/history", "", map[string]string{"id": f.run.ID})
	if rec.Code != http.StatusConflict {
		t.Fatalf("no session = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRunHistoryHidesSupportRunsFromSessions(t *testing.T) {
	f := newNativeFixture(t, "http://127.0.0.1:1")
	f.run, _ = f.server.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		r.SessionID = "sess-1"
		r.Purpose = "verifier"
	})
	req := httptest.NewRequest(http.MethodGet, "/api/runs/"+f.run.ID+"/history", nil)
	req.SetPathValue("id", f.run.ID)
	req = req.WithContext(WithAgentSurface(context.Background()))
	rec := httptest.NewRecorder()
	f.server.runHistory(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("support run read by session = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRunCommandsExposesNativeMenu(t *testing.T) {
	rpc := &nativeSessionRPC{}
	srv := testutil.NewServer(t, rpc.handler(t))
	defer srv.Close()
	f := newNativeFixture(t, srv.URL)
	rec := f.call(t, f.server.runCommands, http.MethodGet, "/api/runs/"+f.run.ID+"/commands", "", map[string]string{"id": f.run.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("commands = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Commands []struct {
			Name        string   `json:"name"`
			Usage       string   `json:"usage"`
			Aliases     []string `json:"aliases"`
			Description string   `json:"description"`
		} `json:"commands"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Commands) != 2 || out.Commands[0].Name != "help" || out.Commands[0].Usage != "/help" || out.Source != "aop.ListCommands" {
		t.Fatalf("commands = %s", rec.Body.String())
	}
	if rpc.listCommands["sessionId"] != "sess-1" {
		t.Fatalf("ListCommands request = %v", rpc.listCommands)
	}
}

func TestRunMediaServesPreservedFileWithRange(t *testing.T) {
	f := newNativeFixture(t, "")
	mediaDir := filepath.Join(f.server.CaseRoot, f.intent.ID, filepath.FromSlash(aopexec.MediaDir))
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3, 4}
	if err := os.WriteFile(filepath.Join(mediaDir, "ev-1-0.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, f.server.runMedia, http.MethodGet, "/api/runs/"+f.run.ID+"/media/ev-1-0.png", "", map[string]string{"id": f.run.ID, "name": "ev-1-0.png"})
	if rec.Code != http.StatusOK {
		t.Fatalf("media = %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff = %q", got)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	if string(rec.Body.Bytes()) != string(png) {
		t.Fatalf("body = %v", rec.Body.Bytes())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/runs/"+f.run.ID+"/media/ev-1-0.png", nil)
	req.SetPathValue("id", f.run.ID)
	req.SetPathValue("name", "ev-1-0.png")
	req.Header.Set("Range", "bytes=0-3")
	rangeRec := httptest.NewRecorder()
	f.server.runMedia(rangeRec, req)
	if rangeRec.Code != http.StatusPartialContent || rangeRec.Body.Len() != 4 {
		t.Fatalf("range = %d %d bytes", rangeRec.Code, rangeRec.Body.Len())
	}
}

func TestRunMediaRejectsTraversalAndMissing(t *testing.T) {
	f := newNativeFixture(t, "")
	for name, want := range map[string]int{
		"..\\secret.txt": http.StatusNotFound,
		"../secret.txt":  http.StatusNotFound,
		"ev-1-0.png":     http.StatusNotFound,
		"":               http.StatusNotFound,
	} {
		rec := f.call(t, f.server.runMedia, http.MethodGet, "/api/runs/x/media/"+name, "", map[string]string{"id": f.run.ID, "name": name})
		if rec.Code != want {
			t.Fatalf("media name %q = %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestRunMediaRejectsOversizedFile(t *testing.T) {
	f := newNativeFixture(t, "")
	mediaDir := filepath.Join(f.server.CaseRoot, f.intent.ID, filepath.FromSlash(aopexec.MediaDir))
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaDir, "big.bin"), make([]byte, aopexec.MaxMediaBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, f.server.runMedia, http.MethodGet, "/api/runs/"+f.run.ID+"/media/big.bin", "", map[string]string{"id": f.run.ID, "name": "big.bin"})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized media = %d %s", rec.Code, rec.Body.String())
	}
}

// A resume that hit a lost session first asks the host for a replacement
// session; when ResetSession succeeds the retry continues that session instead
// of opening another fresh one.
func TestResumeGoneRetriesOnResetSession(t *testing.T) {
	rpc := &nativeSessionRPC{resetAccepted: true}
	srv := testutil.NewServer(t, rpc.handler(t))
	defer srv.Close()
	f := newNativeFixture(t, srv.URL)
	work := filepath.Join(f.server.CaseRoot, f.intent.ID)
	if err := os.MkdirAll(filepath.Join(work, ".tw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".tw", "aop-session.json"),
		[]byte(`{"url":"`+srv.URL+`","agent":"local","session":"sess-gone"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f.run, _ = f.server.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		r.Status = model.RunFailed
		r.SessionID = "sess-gone"
	})
	if _, err := f.server.Store.UpdateIntent(f.intent.ID, func(i *model.Intent) {
		i.Status = model.StatusFailed
		i.RunID = f.run.ID
	}); err != nil {
		t.Fatal(err)
	}
	f.server.fallbackToFreshSession(f.run.ID, f.intent.ID)
	waitFor(t, 5*time.Second, func() bool { return len(f.exec.requests()) == 1 })
	if req := f.exec.requests()[0]; !req.Resume {
		t.Fatal("after a successful ResetSession the retry must continue the replacement session")
	}
	adopted, ok := aopexec.ReadSession(work)
	if !ok || adopted.Session != "sess-new" {
		t.Fatalf("case marker not adopted: %+v ok=%v", adopted, ok)
	}
	old, _ := f.server.Store.GetRun(f.run.ID)
	if len(old.Warnings) == 0 || !strings.Contains(old.Warnings[0], "ResetSession 已建立替代会话") {
		t.Fatalf("reset outcome not recorded: %+v", old.Warnings)
	}
	if len(rpc.resetRequests) != 1 || rpc.resetRequests[0]["sessionId"] != "sess-gone" {
		t.Fatalf("ResetSession requests = %v", rpc.resetRequests)
	}
}

// When the host rejects ResetSession (the usual NOT_FOUND), the existing fresh
// fallback stays, with the host's reason recorded on the old run.
func TestResumeGoneResetRejectionKeepsFreshFallback(t *testing.T) {
	rpc := &nativeSessionRPC{resetAccepted: false}
	srv := testutil.NewServer(t, rpc.handler(t))
	defer srv.Close()
	f := newNativeFixture(t, srv.URL)
	f.run, _ = f.server.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		r.Status = model.RunFailed
		r.SessionID = "sess-gone"
	})
	if _, err := f.server.Store.UpdateIntent(f.intent.ID, func(i *model.Intent) {
		i.Status = model.StatusFailed
		i.RunID = f.run.ID
	}); err != nil {
		t.Fatal(err)
	}
	f.server.fallbackToFreshSession(f.run.ID, f.intent.ID)
	waitFor(t, 5*time.Second, func() bool { return len(f.exec.requests()) == 1 })
	if req := f.exec.requests()[0]; req.Resume {
		t.Fatal("a rejected ResetSession must fall back to a fresh session, not a resume")
	}
	old, _ := f.server.Store.GetRun(f.run.ID)
	if len(old.Warnings) == 0 || !strings.Contains(old.Warnings[0], "ResetSession 无法恢复") || !strings.Contains(old.Warnings[0], "原会话") {
		t.Fatalf("reset rejection not recorded: %+v", old.Warnings)
	}
}
