package node

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kksty/saker/internal/aopexec"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/host"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A real Hub and two real scan processes, with a local synthetic model only.
func TestNativeMultiScanDispatchCancelAndResume(t *testing.T) {
	hubBin, scanBin := os.Getenv("SAKER_HARNESS_BIN"), os.Getenv("SAKER_SCAN_BIN")
	if hubBin == "" || scanBin == "" {
		t.Skip("set SAKER_HARNESS_BIN and SAKER_SCAN_BIN for native multi-node integration")
	}
	blocked := make(chan struct{})
	var blockedOnce sync.Once
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream   bool   `json:"stream"`
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		last := ""
		for _, m := range request.Messages {
			if m.Role == "user" {
				last = string(m.Content)
			}
		}
		if strings.Contains(last, "SAKER-MULTI-CANCEL") {
			blockedOnce.Do(func() { close(blocked) })
			<-r.Context().Done()
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"fixture complete\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"total_tokens\":11}}\n\ndata: [DONE]\n\n", request.Model)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"fixture complete"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`))
	}))
	defer gateway.Close()
	root := t.TempDir()
	config := filepath.Join(root, "fixture.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("llm:\n  provider: openai\n  base_url: %s/v1\n  api_key: fixture-key\n  model: fixture\n", gateway.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	hub, err := host.Start(ctx, host.Config{Bin: hubBin, NodeBin: scanBin, Dir: root, Args: []string{"--config", config}, DropEnvPrefixes: []string{"CYBER_"}, RequireProvider: true, ReadyTimeout: 30 * time.Second, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Stop()
	u, _ := url.Parse(hub.Endpoint())
	u.User = url.User(hub.Token())
	const worker = "11223344556677889900aabbccddeeff"
	child := exec.CommandContext(ctx, scanBin, "agent", "--transport", "web", "--server-url", u.String(), "--node-id", worker, "--node-name", "fixture-worker", "--config", config, "--timeout", "0")
	child.Dir = root
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "CYBER_") {
			child.Env = append(child.Env, entry)
		}
	}
	output, err := os.Create(filepath.Join(root, "worker.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	child.Stdout = output
	child.Stderr = output
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	registry, err := New([]Spec{{ID: "local", AOP: hub.Endpoint(), Token: hub.Token(), Agent: hub.Agent()}}, "local")
	if err != nil {
		t.Fatal(err)
	}
	registry.SetLocalID("local")
	found := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		registry.discoveryAt = time.Time{}
		_ = registry.RefreshNodes(ctx)
		for _, n := range registry.Infos() {
			if n.ID == "scan_"+worker && n.Ready != nil && *n.Ready {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !found {
		raw, _ := os.ReadFile(filepath.Join(root, "worker.log"))
		t.Fatalf("second scan not ready: %s", raw)
	}
	router := registry.Executor(0).(*Router)
	work := filepath.Join(root, "project", "case")
	_ = os.MkdirAll(work, 0700)
	emit := func(executor.Event) {}
	req := executor.Request{IntentID: "fixture", Node: "scan_" + worker, WorkDir: work, Prompt: "SAKER-MULTI-FIRST: return fixture complete", MaxTurns: 1}
	if _, err = router.Run(ctx, req, emit); err != nil {
		t.Fatal(err)
	}
	first, ok := aopexec.ReadSession(work)
	if !ok || first.Agent != worker {
		t.Fatalf("wrong scan marker: %+v", first)
	}
	cancelCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()
	done := make(chan error, 1)
	req.Resume = true
	req.Prompt = "SAKER-MULTI-CANCEL"
	go func() { _, e := router.Run(cancelCtx, req, emit); done <- e }()
	select {
	case <-blocked:
		cancelTurn()
	case <-ctx.Done():
		t.Fatal("fixture model never received cancellation turn")
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("canceled turn succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scan cancellation did not settle")
	}
	if !router.CanResume(req.Node, work) {
		t.Fatal("cancellation lost the resumable session")
	}
	req.Prompt = "SAKER-MULTI-RESUME: return fixture complete"
	if _, err = router.Run(ctx, req, emit); err != nil {
		t.Fatal(err)
	}
	after, ok := aopexec.ReadSession(work)
	if !ok || after.Session != first.Session || after.Agent != worker {
		t.Fatalf("resume moved session: %+v -> %+v", first, after)
	}
}
