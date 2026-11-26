package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kksty/saker/internal/testutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/saker/internal/access"
	"github.com/kksty/saker/internal/aop"
	"github.com/kksty/saker/internal/aopexec"
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/decider"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/host"
	"github.com/kksty/saker/internal/ioa"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/node"
	"github.com/kksty/saker/internal/skills"
	"github.com/kksty/saker/internal/ws"
)

func TestHarnessProjectPauseCancelsModelAndPreventsFurtherRequests(t *testing.T) {
	bin := os.Getenv("SAKER_HARNESS_BIN")
	if bin == "" {
		bin = os.Getenv("SAKER_AISCAN_BIN")
	}
	scan := os.Getenv("SAKER_SCAN_BIN")
	if bin == "" || scan == "" {
		t.Skip("set SAKER_HARNESS_BIN and SAKER_SCAN_BIN for real pause/model cancellation check")
	}
	var calls atomic.Int32
	started, disconnected := make(chan struct{}), make(chan struct{})
	var startOnce, stopOnce sync.Once
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages json.RawMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		calls.Add(1)
		if strings.Contains(string(req.Messages), "TW-PAUSE-MODEL-TEST") {
			startOnce.Do(func() { close(started) })
			<-r.Context().Done()
			stopOnce.Do(func() { close(disconnected) })
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer gateway.Close()
	s, _, _ := newCommanderServer(t)
	// The native host only admits workspaces beneath its own working dir.
	dir := filepath.Dir(s.DataDir)
	if _, err := skills.Install(skills.Commander, filepath.Join(dir, ".cyber", "skills")); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "fixture.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("llm:\n  provider: openai\n  base_url: %s/v1\n  api_key: fixture-key\n  model: fixture-model\n", gateway.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h, err := host.Start(ctx, host.Config{Bin: bin, NodeBin: scan, Dir: dir, Args: []string{"--config", config}, DropEnvPrefixes: []string{"CYBER_", "SAKER_CONSOLE_TOKEN", "SAKER_AGENT_TOKEN"}, RequireProvider: true, ReadyTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	s.Exec = &aopexec.Executor{BaseURL: h.Endpoint(), Token: h.Token(), Agent: h.Agent()}
	s.LocalNode, s.LocalHostStatus = "local", h.Status
	r := decider.NewRunner(s.Store, nil, "")
	r.ModelName = "fixture-model"
	s.Decider = r
	r.Commander = func(ctx context.Context, prompt string, resume bool) (decider.RoundResult, error) {
		res, actions, err := s.StartCommander(ctx, withBoardAnchor(prompt+"\nTW-PAUSE-MODEL-TEST"), resume)
		return decider.RoundResult{Tokens: res.Tokens, Actions: actions}, err
	}
	done := make(chan *decider.Decision, 1)
	go func() { done <- r.Run(ctx, "operator", true) }()
	select {
	case <-started:
	case d := <-done:
		t.Fatalf("commander stopped before reaching model: %+v", d)
	case <-ctx.Done():
		t.Fatal("commander never reached model")
	}
	if rec := call(t, s, http.MethodPost, "/api/project/pause", nil); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pause did not stop commander round")
	}
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("pause did not cancel provider HTTP request")
	}
	before := calls.Load()
	for n := 0; n < 100; n++ {
		r.Wake("operator")
		r.Wake("message")
		if r.Run(ctx, "heartbeat", true) != nil {
			t.Fatal("paused commander ran")
		}
	}
	if calls.Load() != before {
		t.Fatalf("paused project issued model requests: %d -> %d", before, calls.Load())
	}
	for _, run := range s.Store.Runs() {
		if run.Purpose == "commander" && (run.Status != model.RunCanceled || run.CancelCause != model.CancelCausePause) {
			t.Fatalf("pause run status=%s cause=%s", run.Status, run.CancelCause)
		}
	}
}

func TestHarnessModelFailureKeepsConsoleTerminalAndRecovers(t *testing.T) {
	bin := os.Getenv("SAKER_HARNESS_BIN")
	if bin == "" {
		bin = os.Getenv("SAKER_AISCAN_BIN")
	}
	scan := os.Getenv("SAKER_SCAN_BIN")
	if bin == "" || scan == "" {
		t.Skip("set SAKER_HARNESS_BIN and SAKER_SCAN_BIN for the real host/PTY contract check")
	}
	var paid atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !paid.Load() {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"message":"Insufficient Balance","type":"insufficient_balance_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer gateway.Close()
	dir := t.TempDir()
	config := filepath.Join(dir, "fixture.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("llm:\n  provider: openai\n  base_url: %s/v1\n  api_key: fixture-key\n  model: fixture-model\n", gateway.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{"--config", config}
	h, err := host.Start(ctx, host.Config{Bin: bin, NodeBin: scan, Dir: dir, Args: args, DropEnvPrefixes: []string{"CYBER_", "SAKER_CONSOLE_TOKEN", "SAKER_AGENT_TOKEN"}, RequireProvider: true, AllowNotReady: true, ReadyTimeout: 10 * time.Second, Logf: t.Logf})
	if h == nil || !errors.Is(err, host.ErrNotReady) {
		t.Fatalf("failed model Start = %v, host=%v", err, h)
	}
	defer h.Stop()
	if status := h.Status(); !status.Online || status.Ready || !strings.Contains(status.LastError, "Insufficient Balance") {
		t.Fatalf("degraded status=%+v", status)
	}
	s, _, _ := newCommanderServer(t)
	registry, err := node.New([]node.Spec{{ID: "local", AOP: h.Endpoint(), Token: h.Token(), Agent: h.Agent()}}, "local")
	if err != nil {
		t.Fatal(err)
	}
	s.Exec, s.LocalNode, s.LocalHostStatus = registry.Executor(0), "local", h.Status
	if _, _, err := s.StartCommander(ctx, withBoardAnchor("should not run"), false); err == nil || !strings.Contains(err.Error(), "Insufficient Balance") {
		t.Fatalf("commander refusal=%v", err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	console := httptest.NewServer(mux)
	defer console.Close()
	sock, err := testutil.RetryConnect(t, "terminal bridge", func() (*ws.Conn, error) {
		return ws.Dial(ctx, "ws"+strings.TrimPrefix(console.URL, "http")+"/api/nodes/local/terminal", nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	// Closing on timeout bounds the blocking WebSocket reader too.
	stop := context.AfterFunc(ctx, func() { _ = sock.Close() })
	defer stop()
	if err := sock.WriteBinary([]byte("echo TW-PTY-AVAILABLE\r\n")); err != nil {
		t.Fatal(err)
	}
	var output string
	for !strings.Contains(output, "TW-PTY-AVAILABLE") {
		kind, data, err := sock.ReadMessageType()
		if err != nil {
			t.Fatalf("terminal unavailable while model failed: %v; output=%q", err, output)
		}
		if kind == ws.OpText {
			t.Fatalf("terminal error: %s", data)
		}
		output += string(data)
	}
	paid.Store(true)
	if !h.Update(nil, args) {
		t.Fatal("unchanged settings did not retry failed model")
	}
	for !h.Ready() && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if !h.Ready() {
		t.Fatal("host did not become ready after balance recovered")
	}
}

// This checks the composed API -> AOP -> real harness -> provider boundary.
// A fake AOP host cannot detect a syntactically valid but unexpanded /skill.
// No external model or target is used. Opt in with SAKER_HARNESS_BIN (+ SAKER_SCAN_BIN).
func TestHarnessExpandsCommanderSkillBeforeCallingModel(t *testing.T) {
	bin := os.Getenv("SAKER_HARNESS_BIN")
	if bin == "" {
		bin = os.Getenv("SAKER_AISCAN_BIN")
	}
	scan := os.Getenv("SAKER_SCAN_BIN")
	if bin == "" || scan == "" {
		t.Skip("set SAKER_HARNESS_BIN and SAKER_SCAN_BIN to run the real harness contract check")
	}
	parallelStarted := make(chan struct{}, 2)
	parallelRelease := make(chan struct{})
	releaseParallel := sync.OnceFunc(func() { close(parallelRelease) })
	defer releaseParallel()
	var mu sync.Mutex
	var inputs []string
	modelTools := map[string]bool{}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string          `json:"model"`
			Stream   bool            `json:"stream"`
			Messages json.RawMessage `json:"messages"`
			Tools    []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		inputs = append(inputs, string(req.Messages))
		if strings.Contains(string(req.Messages), "HARNESS-NATIVE-SKILL") {
			for _, tool := range req.Tools {
				modelTools[tool.Function.Name] = true
			}
		}
		mu.Unlock()
		if strings.Contains(string(req.Messages), "SPLIT-PARALLEL-") {
			parallelStarted <- struct{}{}
			select {
			case <-parallelRelease:
			case <-r.Context().Done():
				return
			}
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"done\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"total_tokens\":11}}\n\ndata: [DONE]\n\n", req.Model, req.Model)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"fixture","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`, req.Model)
	}))
	defer gateway.Close()
	s, _, _ := newCommanderServer(t)
	dir := filepath.Dir(s.DataDir)
	if _, err := skills.Install(skills.Commander, filepath.Join(dir, ".cyber", "skills")); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "fixture.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("llm:\n  active_profile: fixture\n  providers:\n    - id: fixture\n      provider: openai\n      base_url: %s/v1\n      api_key: fixture-key\n      model: fixture-model\n", gateway.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	h, err := host.Start(ctx, host.Config{Bin: bin, NodeBin: scan, Dir: dir, Args: []string{"--config", config}, DropEnvPrefixes: []string{"CYBER_"}, ExcludeTools: "subagent", ReadyTimeout: 45 * time.Second, RequireProvider: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	registry, err := node.New([]node.Spec{{ID: "local", AOP: h.Endpoint(), Token: h.Token(), Agent: h.Agent()}}, "local")
	if err != nil {
		t.Fatal(err)
	}
	s.Exec = registry.Executor(0)
	if _, _, err := s.StartCommander(ctx, withBoardAnchor("HARNESS-NATIVE-SKILL 合约检查"), false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	found := false
	for _, input := range inputs {
		if !strings.Contains(input, "HARNESS-NATIVE-SKILL") {
			continue
		}
		if !strings.Contains(input, "你是这个项目的指挥官") || strings.Contains(input, "/skill:commander ") || !strings.Contains(input, "工作目录") {
			t.Fatal("provider received the task without an expanded commander skill and workspace")
		}
		found = true
	}
	modelCalls := len(inputs)
	for _, name := range []string{"bash", "read", "write", "ls", "glob"} {
		if !modelTools[name] {
			t.Errorf("commander did not receive native harness tool %q", name)
		}
	}
	if modelTools["subagent"] {
		t.Error("native subagent bypasses the tracked control-plane worker lifecycle")
	}
	mu.Unlock()
	if !found {
		t.Fatal("the real harness never sent the commander task to the fixture model")
	}

	// rc10 uses a hex AOP id and IOA must accept the same discovered identity.
	for _, selector := range []string{"", "local", h.Agent()} {
		resolved, err := aop.New(h.Endpoint(), h.Token()).NodeID(ctx, selector)
		if err != nil || resolved != h.Agent() {
			t.Fatalf("resolve %q = %q, %v", selector, resolved, err)
		}
	}
	marker, ok := aopexec.ReadSession(s.CommanderDir())
	if !ok {
		t.Fatal("commander session marker missing")
	}
	if _, err := ioa.New(h.Endpoint(), h.Token(), h.Agent()).SendEvent(ctx, marker.Session, "SPLIT-IOA-MESSAGE", false, "split-ioa-contract"); err != nil {
		t.Fatal(err)
	}
	// IOA acceptance precedes asynchronous inbox delivery. Start only explicitly
	// tracked fixture turns, without resending, until the accepted input arrives.
	sawIOA := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if _, _, err := s.StartCommander(ctx, withBoardAnchor("HARNESS-IOA-ROUND"), true); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		for _, input := range inputs {
			if strings.Contains(input, "HARNESS-IOA-ROUND") && strings.Contains(input, "SPLIT-IOA-MESSAGE") {
				sawIOA = true
			}
		}
		modelCalls = len(inputs)
		mu.Unlock()
		if sawIOA {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !sawIOA {
		t.Fatal("IOA accepted the message but it never reached the continued session")
	}

	t.Run("one scan runs independent sessions concurrently", func(t *testing.T) {
		defer releaseParallel()
		turnCtx, cancelTurns := context.WithTimeout(ctx, 10*time.Second)
		defer cancelTurns()
		finished := make(chan error, 2)
		dirs := []string{filepath.Join(s.CaseRoot, "parallel-a"), filepath.Join(s.CaseRoot, "parallel-b")}
		for i, dir := range dirs {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			go func(i int, dir string) {
				result, err := s.Exec.Run(turnCtx, executor.Request{IntentID: fmt.Sprintf("parallel-%d", i), WorkDir: dir, Prompt: fmt.Sprintf("SPLIT-PARALLEL-%d", i)}, func(executor.Event) {})
				if err == nil && (result.IsError || result.ExitCode != 0) {
					err = fmt.Errorf("failed turn: %+v", result)
				}
				finished <- err
			}(i, dir)
		}
		// Neither model call returns until both sessions have reached the gateway.
		for range 2 {
			select {
			case <-parallelStarted:
			case <-turnCtx.Done():
				t.Fatal("scan serialized independent sessions")
			}
		}
		releaseParallel()
		for range 2 {
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
		}
		first, okA := aopexec.ReadSession(dirs[0])
		second, okB := aopexec.ReadSession(dirs[1])
		if !okA || !okB || first.Session == second.Session || first.Agent != h.Agent() || second.Agent != h.Agent() {
			t.Fatal("parallel runs did not have separate sessions on the same scan")
		}
	})
	mu.Lock()
	modelCalls = len(inputs)
	mu.Unlock()

	// The native browser control path must resolve the node through the
	// production Router. Use a local HTTP fixture instead of opening a browser
	// window; this checks command execution without consuming a model turn.
	target := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if account := r.URL.Query().Get("account"); account != "" {
			http.SetCookie(w, &http.Cookie{Name: "fixture_account", Value: account, Path: "/"})
		}
		_, _ = w.Write([]byte("NATIVE-PROFILE-HTTP-FIXTURE"))
	}))
	defer target.Close()
	defaultRegistry, err := node.New([]node.Spec{{ID: "local", AOP: h.Endpoint(), Token: h.Token(), Agent: h.Agent()}}, "local")
	if err != nil {
		t.Fatal(err)
	}
	s.Exec, s.LocalNode = defaultRegistry.Executor(0), "local"
	catalogue := httptest.NewRecorder()
	s.listRuntimeTools(catalogue, httptest.NewRequest(http.MethodGet, "/api/tools/runtime", nil))
	var declared runtimeToolsResponse
	if err := json.Unmarshal(catalogue.Body.Bytes(), &declared); err != nil {
		t.Fatal(err)
	}
	if declared.State != "connected" {
		t.Fatalf("live harness catalogue unavailable: %s", declared.Error)
	}
	available := map[string]bool{}
	for _, command := range declared.Commands {
		available[command.Name] = true
	}
	for _, name := range []string{"!curl", "!arsenal"} {
		if !available[name] {
			t.Errorf("live harness catalogue did not declare %q", name)
		}
	}
	s.Exec = registry.Executor(0)
	entry, err := access.Create(s.CaseRoot, access.Input{Name: "fixture", Kind: "browser", Origin: target.URL})
	if err != nil {
		t.Fatal(err)
	}
	output, err := s.runBrowserCommand(ctx, entry, "!curl "+quoteNative(target.URL))
	if err != nil || !strings.Contains(output, "NATIVE-PROFILE-HTTP-FIXTURE") {
		t.Fatalf("native profile command = %q, %v", output, err)
	}
	if os.Getenv("SAKER_BROWSER_INTEGRATION") == "1" {
		t.Run("registered browser ownership", func(t *testing.T) {
			operatorLease := leaseID(t, leaseCall(t, s, entry, nil, map[string]any{"action": "acquire"}))
			if _, err := s.runBrowserCommand(ctx, entry, "!playwright open "+quoteNative(target.URL)+" --session "+quoteNative(entry.Session)); err != nil {
				t.Fatal(err)
			}
			defer func() { _, _ = s.runBrowserCommand(ctx, entry, "!playwright close "+quoteNative(entry.Session)) }()
			mux := http.NewServeMux()
			s.Register(mux)
			inspect := func(lease string) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/access/"+entry.ID+"/browser", strings.NewReader(`{"action":"inspect","lease_id":"`+lease+`"}`)))
				return rec
			}
			if rec := inspect(operatorLease); rec.Code != 200 || !strings.Contains(rec.Body.String(), target.URL) {
				t.Fatalf("real browser inspect = %d %s", rec.Code, rec.Body.String())
			}
			if rec := leaseCall(t, s, entry, nil, map[string]any{"action": "release", "lease_id": operatorLease}); rec.Code != 200 {
				t.Fatal(rec.Body.String())
			}
			in, err := s.Store.CreateIntent(model.Intent{Title: "浏览器占用验证"})
			if err != nil {
				t.Fatal(err)
			}
			claimed, run, err := s.Store.StartIntentRun(in.ID, "local", "", model.Run{Profile: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			worker := collab.Identity{IntentID: in.ID, RunID: run.ID, ParticipantID: collab.ParticipantID(in.ID), Generation: claimed.Attempts}
			workerLease := leaseID(t, leaseCall(t, s, entry, &worker, map[string]any{"action": "acquire"}))
			if rec := inspect(operatorLease); rec.Code != 409 {
				t.Fatal("operator accessed a real browser owned by a worker")
			}
			if rec := leaseCall(t, s, entry, &worker, map[string]any{"action": "release", "lease_id": workerLease}); rec.Code != 200 {
				t.Fatal(rec.Body.String())
			}
			fresh := leaseID(t, leaseCall(t, s, entry, nil, map[string]any{"action": "acquire"}))
			if rec := inspect(operatorLease); rec.Code != 409 {
				t.Fatal("old acquisition accessed a newly acquired browser")
			}
			if rec := inspect(fresh); rec.Code != 200 || !strings.Contains(rec.Body.String(), target.URL) {
				t.Fatal("browser state lost during ownership handoff")
			}
			control, err := s.browserControlState(entry.ID)
			if err != nil {
				t.Fatal(err)
			}
			control.Pending = true // Simulate losing the completion after a real operation.
			raw, _ := json.Marshal(control)
			if err := os.WriteFile(filepath.Join(s.CaseRoot, ".access", entry.ID, "control-session.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if rec := leaseCall(t, s, entry, nil, map[string]any{"action": "recover", "lease_id": fresh, "confirmed_idle": true, "note": "已检查本地测试页面无在途操作"}); rec.Code != 200 {
				t.Fatal(rec.Body.String())
			}
			recovered := leaseID(t, leaseCall(t, s, entry, nil, map[string]any{"action": "acquire"}))
			if rec := inspect(recovered); rec.Code != 200 || !strings.Contains(rec.Body.String(), target.URL) {
				t.Fatal("confirmed recovery lost the real browser page")
			}
		})
		t.Run("worker login challenge survives repeated console open", func(t *testing.T) {
			registered, err := access.Create(s.CaseRoot, access.Input{Kind: "browser", Origin: target.URL + "/login", Session: "worker_captcha"})
			if err != nil {
				t.Fatal(err)
			}
			challenge := target.URL + "/captcha?challenge=keep-this-page"
			if _, err := s.runBrowserCommand(ctx, registered, "!playwright open "+quoteNative(challenge)+" --session "+quoteNative(registered.Session)); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_, _ = s.runBrowserCommand(ctx, registered, "!playwright close "+quoteNative(registered.Session))
			}()
			lease := leaseID(t, leaseCall(t, s, registered, nil, map[string]any{"action": "acquire"}))
			for i := 0; i < 2; i++ {
				rec := accessHandoffCall(s, nil, "POST", "/api/access/"+registered.ID+"/browser", `{"action":"open","lease_id":"`+lease+`"}`)
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "keep-this-page") {
					t.Fatalf("challenge was reopened or lost: %d %s", rec.Code, rec.Body.String())
				}
			}
		})
		t.Run("same site account sessions keep separate cookies", func(t *testing.T) {
			for _, account := range []string{"A", "B"} {
				browser, err := access.Create(s.CaseRoot, access.Input{Name: "账号 " + account, Kind: "browser", Origin: target.URL, Session: "account_" + account})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.runBrowserCommand(ctx, browser, "!playwright open "+quoteNative(target.URL+"?account="+account)+" --session "+quoteNative(browser.Session)); err != nil {
					t.Fatal(err)
				}
				defer func() { _, _ = s.runBrowserCommand(ctx, browser, "!playwright close "+quoteNative(browser.Session)) }()
			}
			rows, _ := access.List(s.CaseRoot)
			for _, browser := range rows {
				if browser.Session != "account_A" && browser.Session != "account_B" {
					continue
				}
				if _, err := s.runBrowserCommand(ctx, browser, "!playwright state-save "+quoteNative(browser.Session)+" account.json"); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filepath.Join(s.CaseRoot, ".access", browser.ID, "account.json"))
				if err != nil {
					t.Fatal(err)
				}
				var state struct {
					Cookies []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"cookies"`
				}
				if err := json.Unmarshal(raw, &state); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, cookie := range state.Cookies {
					if cookie.Name == "fixture_account" {
						found = true
						if cookie.Value != strings.TrimPrefix(browser.Session, "account_") {
							t.Fatalf("account cookies leaked across sessions: %s has %s", browser.Session, cookie.Value)
						}
					}
				}
				if !found {
					t.Fatalf("missing login fixture cookie for %s", browser.Session)
				}
			}
		})
	}
	mu.Lock()
	defer mu.Unlock()
	if len(inputs) != modelCalls {
		t.Fatal("native control unexpectedly called the model")
	}
}
