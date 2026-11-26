package host

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Both crash directions must replace the pair without changing its node ID.
// The binaries, configuration, sessions and gateway are isolated from the app.
func TestSplitHostRecoversBothProcesses(t *testing.T) {
	bin, nodeBin := os.Getenv("SAKER_HARNESS_BIN"), os.Getenv("SAKER_SCAN_BIN")
	if bin == "" || nodeBin == "" {
		t.Skip("set SAKER_HARNESS_BIN and SAKER_SCAN_BIN for split-process recovery")
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer gateway.Close()
	dir := t.TempDir()
	config := filepath.Join(dir, "model.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("llm:\n  provider: openai\n  base_url: %s/v1\n  api_key: fixture-key\n  model: fixture\n", gateway.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := Start(ctx, Config{Bin: bin, NodeBin: nodeBin, Dir: dir,
		Args: []string{"--config", config}, DropEnvPrefixes: []string{"CYBER_"},
		RequireProvider: true, Persist: true, RetryBackoff: 100 * time.Millisecond,
		ReadyTimeout: 30 * time.Second, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	for _, victim := range []string{"scan", "hub"} {
		t.Run(victim, func(t *testing.T) {
			before := h.Status()
			pid := before.PID
			if victim == "scan" {
				pid = before.NodePID
			}
			if pid <= 0 || before.PID == before.NodePID || !before.Ready || len(before.Agent) != 32 {
				t.Fatalf("invalid ready pair: %+v", before)
			}
			p, err := os.FindProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Kill(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(40 * time.Second)
			for time.Now().Before(deadline) {
				after := h.Status()
				if after.Ready && after.PID != before.PID && after.NodePID != before.NodePID {
					if after.Agent != before.Agent || after.Endpoint != before.Endpoint || after.Restarts <= before.Restarts {
						t.Fatalf("recovery changed identity: before=%+v after=%+v", before, after)
					}
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Fatalf("%s exit did not recover pair: %+v", victim, h.Status())
		})
	}
}
