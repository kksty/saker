package api

// P0-3 real-harness round-trip: a real cyber-harness Hub+scan pair, a local
// scripted model gateway, and the control plane's actual note endpoint. The
// scripted model performs the same protocol steps a real commander takes
// through the harness bash tool:
//
//	GET  /p/<project>/api/state   with the per-round credential -> read run_id/version
//	PUT  /p/<project>/api/state   with the same credential        -> commit the note
//
// The control plane then has to accept the committed version as that round's
// handoff receipt, and the following pressure round must not repeat the handoff.
// The gateway is scripted so the run is deterministic; the harness, the tool
// execution path and the note API are the real ones. Opt in with
// SAKER_HARNESS_BIN (and SAKER_SCAN_BIN); otherwise the test skips, like the other
// real-harness integration tests.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/saker/internal/aopexec"
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/decider"
	"github.com/kksty/saker/internal/host"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/skills"
)

// handoffHarnessCall is one provider request seen by the scripted gateway.
type handoffHarnessCall struct {
	lastUser   string
	toolResult string
	stream     bool
}

// handoffHarnessGateway serves deterministic OpenAI-compatible responses: it
// reads the state through the harness bash tool, then writes the note through
// the same tool, exactly following the protocol the prompt asked for. It only
// scripts *what* the model says, not how the control plane or harness behave.
type handoffHarnessGateway struct {
	stateURL string
	logf     func(string, ...any)

	mu       sync.Mutex
	calls    []handoffHarnessCall
	commands []string
	runIDs   []string
}

func (g *handoffHarnessGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Stream   bool              `json:"stream"`
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(raw, &req)
	call := handoffHarnessCall{stream: req.Stream}
	for _, message := range req.Messages {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(message, &m) != nil {
			continue
		}
		text := ""
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			text = s
		} else {
			var parts []struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(m.Content, &parts) == nil {
				for _, part := range parts {
					text += part.Text
				}
			}
		}
		switch m.Role {
		case "tool":
			// Keep the latest tool result: after the GET the model writes, and
			// after the PUT it ends the round. Older tool results stay in the
			// history, so the switch below must look only at the newest one.
			call.toolResult = text
		case "user":
			if strings.TrimSpace(text) != "" {
				call.lastUser = text
			}
		}
	}
	g.mu.Lock()
	g.calls = append(g.calls, call)
	g.mu.Unlock()
	if g.logf != nil {
		g.logf("gateway: handoff_prompt=%v tool_result=%q", strings.Contains(call.lastUser, "本轮交接；"), handoffHarnessClip(call.toolResult, 240))
	}

	g.mu.Lock()
	issued := len(g.commands)
	g.mu.Unlock()
	if issued > 8 {
		// A broken scripted protocol must fail the test fast instead of looping
		// until the harness turn guard ends the round with a generic error.
		handoffHarnessWriteText(w, req.Stream, "protocol loop")
		return
	}
	switch {
	case strings.Contains(call.toolResult, `"saved":true`):
		// The PUT committed the note; the round's work is done.
		handoffHarnessWriteText(w, req.Stream, "done")
	case strings.Contains(call.toolResult, `"run_id"`) && strings.Contains(call.toolResult, `"version"`):
		// The model read the current note; now write the handoff version. The
		// version and run_id come from the GET response the harness returned,
		// not from test constants: this is the same read-then-CAS sequence the
		// prompt contract describes.
		runID := handoffHarnessFind(call.toolResult, `"run_id"\s*:\s*"([^"]+)"`)
		version := handoffHarnessFind(call.toolResult, `"version"\s*:\s*(\d+)`)
		if runID == "" || version == "" {
			handoffHarnessWriteText(w, req.Stream, "could not read the note")
			return
		}
		g.mu.Lock()
		g.runIDs = append(g.runIDs, runID)
		g.mu.Unlock()
		body := fmt.Sprintf(`{"state":"handoff note from the real harness round","version":%s,"run_id":"%s"}`, version, runID)
		command := fmt.Sprintf(
			`printf '%%s' %s > .tw/handoff-note.json && curl -s -X PUT -H "$(cat .tw/auth)" -H "Content-Type: application/json" --data-binary @.tw/handoff-note.json %s`,
			handoffHarnessShellQuote(body), handoffHarnessShellQuote(g.stateURL))
		g.record(command)
		handoffHarnessWriteToolCall(w, req.Stream, "call_note_put", "bash", `{"command":`+handoffHarnessJSONString(command)+`}`)
	case strings.Contains(call.lastUser, "本轮交接；"):
		command := `curl -s -H "$(cat .tw/auth)" ` + handoffHarnessShellQuote(g.stateURL)
		g.record(command)
		handoffHarnessWriteToolCall(w, req.Stream, "call_note_get", "bash", `{"command":`+handoffHarnessJSONString(command)+`}`)
	default:
		handoffHarnessWriteText(w, req.Stream, "done")
	}
}

func (g *handoffHarnessGateway) record(command string) {
	g.mu.Lock()
	g.commands = append(g.commands, command)
	g.mu.Unlock()
}

func (g *handoffHarnessGateway) snapshot() ([]handoffHarnessCall, []string, []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]handoffHarnessCall(nil), g.calls...), append([]string(nil), g.commands...), append([]string(nil), g.runIDs...)
}

func handoffHarnessFind(text, pattern string) string {
	match := regexp.MustCompile(pattern).FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func handoffHarnessJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func handoffHarnessShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func handoffHarnessWriteToolCall(w http.ResponseWriter, stream bool, callID, name, arguments string) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"fixture","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%s}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`,
			callID, name, handoffHarnessJSONString(arguments))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":%q,\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]},\"finish_reason\":null}]}\n\n",
		callID, name, handoffHarnessJSONString(arguments))
	_, _ = w.Write([]byte("data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"))
	_, _ = w.Write([]byte("data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":5,\"total_tokens\":25}}\n\n"))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func handoffHarnessWriteText(w http.ResponseWriter, stream bool, text string) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"fixture","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`, text)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n", text)
	_, _ = w.Write([]byte("data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":2,\"total_tokens\":22}}\n\n"))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func handoffHarnessClip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// handoffHarnessConsoleGate is the minimal project-mux gate for this test: the
// production gate (cmd/saker) resolves the bearer token to a collab identity and
// mounts it only on the note path for the commander. The same resolution and
// the same rule are used here; the operator has no token and stays operator.
func handoffHarnessConsoleGate(t *testing.T, s *Server, mux *http.ServeMux) http.Handler {
	t.Helper()
	next := func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/api/state") {
			t.Logf("console: PUT state -> %d %s", rec.Code, handoffHarnessClip(rec.Body.String(), 300))
		}
		for k, values := range rec.Header() {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, "/p/"+s.Project.ID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		r = r.Clone(r.Context())
		r.URL.Path = rest
		hdr := r.Header.Get("Authorization")
		if strings.HasPrefix(hdr, "Bearer ") {
			if ident, ok := s.Collab.Resolve(strings.TrimSpace(strings.TrimPrefix(hdr, "Bearer "))); ok {
				if ident.ProjectID != s.Project.ID {
					http.NotFound(w, r)
					return
				}
				ctx := WithAgentSurface(r.Context())
				if strings.HasSuffix(r.URL.Path, "/api/state") {
					ctx = WithIdentity(ctx, ident)
				}
				r = r.WithContext(ctx)
			}
		}
		next(w, r)
	})
}

func TestHarnessCommanderNoteReceiptRoundTrip(t *testing.T) {
	bin := os.Getenv("SAKER_HARNESS_BIN")
	if bin == "" {
		bin = os.Getenv("SAKER_AISCAN_BIN")
	}
	scan := os.Getenv("SAKER_SCAN_BIN")
	if bin == "" || scan == "" {
		t.Skip("set SAKER_HARNESS_BIN and SAKER_SCAN_BIN to run the real harness handoff round-trip")
	}

	s, _, st := newCommanderServer(t)
	s.Collab = collab.NewRegistry(0)
	mux := http.NewServeMux()
	s.Register(mux)
	console := httptest.NewServer(handoffHarnessConsoleGate(t, s, mux))
	defer console.Close()
	s.APIBase = console.URL

	gateway := &handoffHarnessGateway{stateURL: console.URL + "/p/" + s.Project.ID + "/api/state", logf: t.Logf}
	modelSrv := httptest.NewServer(gateway)
	defer modelSrv.Close()

	dir := filepath.Dir(s.DataDir)
	if _, err := skills.Install(skills.Commander, filepath.Join(dir, ".cyber", "skills")); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "fixture.yaml")
	configBody := fmt.Sprintf("llm:\n  active_profile: fixture\n  providers:\n    - id: fixture\n      provider: openai\n      base_url: %s/v1\n      api_key: fixture-key\n      model: fixture-model\n", modelSrv.URL)
	if err := os.WriteFile(config, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	h, err := host.Start(ctx, host.Config{
		Bin: bin, NodeBin: scan, Dir: dir, Args: []string{"--config", config},
		DropEnvPrefixes: []string{"CYBER_"}, ExcludeTools: "subagent",
		ReadyTimeout: 45 * time.Second, RequireProvider: true, Logf: t.Logf,
	})
	if err != nil {
		t.Fatalf("start real harness: %v", err)
	}
	defer h.Stop()
	s.Exec = &aopexec.Executor{BaseURL: h.Endpoint(), Token: h.Token(), Agent: h.Agent()}
	s.LocalNode, s.LocalHostStatus = "local", h.Status

	r := decider.NewRunner(st, nil, "")
	r.ModelName, r.ContextWindow = "fixture-model", 128000
	var prompts []string
	r.Commander = func(ctx context.Context, prompt string, resume bool) (decider.RoundResult, error) {
		prompts = append(prompts, prompt)
		t.Logf("round start: handoff_prompt=%v resume=%v", strings.Contains(prompt, "本轮交接"), resume)
		res, actions, err := s.StartCommander(ctx, withBoardAnchor(prompt), resume)
		t.Logf("round done: run=%s err=%v", res.LedgerRunID, err)
		// The harness reports its own context; this fixture arms the 85% handoff
		// line deterministically so the second round is the handoff round.
		return decider.RoundResult{RunID: res.LedgerRunID, Tokens: res.Tokens, Actions: actions,
			ContextTokens: 110000, ContextWindow: 128000}, err
	}

	first := r.Run(ctx, "first", true)
	if first == nil {
		t.Fatal("first round did not run")
	}
	if first.Handoff {
		t.Fatalf("first round had no measured pressure yet: %+v", first)
	}

	handoffRound := r.Run(ctx, "handoff", true)
	if handoffRound == nil || handoffRound.HandoffReceipt == nil {
		calls, commands, _ := gateway.snapshot()
		t.Fatalf("real harness handoff round produced no valid receipt: %+v calls=%d commands=%v", handoffRound, len(calls), commands)
	}
	if handoffRound.HandoffMissing != 0 || handoffRound.HandoffReleased {
		t.Fatalf("valid receipt was counted as a failure: %+v", handoffRound)
	}
	receipt := handoffRound.HandoffReceipt
	note, ok := st.Notebook()
	if !ok || note.Writer != "commander" || note.RunID != handoffRound.RunID || note.Version != receipt.Version {
		t.Fatalf("note provenance = %+v, receipt = %+v", note, receipt)
	}
	run, ok := st.GetRun(handoffRound.RunID)
	if !ok || run.Status != model.RunDone || len(run.NoteWrites) != 1 || run.NoteWrites[0] != *receipt {
		t.Fatalf("round receipt = %+v", run)
	}
	if _, ok := st.ValidNoteHandoff(handoffRound.RunID, 0); !ok {
		t.Fatal("ledger rejected the receipt the decider accepted")
	}

	// The scripted provider really executed the note protocol through the
	// harness bash tool: one GET and one PUT, with the round's own run_id.
	calls, commands, runIDs := gateway.snapshot()
	if len(runIDs) == 0 || runIDs[len(runIDs)-1] != handoffRound.RunID {
		t.Fatalf("note write used run %v, want %s; commands=%v", runIDs, handoffRound.RunID, commands)
	}
	var sawGet, sawPut bool
	for _, command := range commands {
		if !strings.Contains(command, "api/state") {
			continue
		}
		if strings.Contains(command, "-X PUT") {
			sawPut = true
		} else {
			sawGet = true
		}
	}
	if !sawGet || !sawPut {
		t.Fatalf("real harness never performed the GET+PUT note protocol: get=%v put=%v calls=%d commands=%v", sawGet, sawPut, len(calls), commands)
	}
	t.Logf("real harness note protocol: GET+PUT through the harness bash tool; run_id=%s note_version=%d", handoffRound.RunID, note.Version)

	// The next pressure round must see the cycle as already handed off, not ask
	// for another handoff and not fabricate a new receipt.
	after := r.Run(ctx, "after", true)
	if after == nil || after.Handoff || after.HandoffReceipt != nil {
		t.Fatalf("handoff repeated after a valid receipt: %+v", after)
	}
	if !strings.Contains(prompts[2], "这个压力周期已经跑过交接轮") {
		t.Fatalf("next round was not told the cycle handed off: %q", prompts[2])
	}
	note, _ = st.Notebook()
	if note.Version != receipt.Version || note.RunID != handoffRound.RunID {
		t.Fatalf("next round rewrote the note without a receipt: %+v", note)
	}
}
