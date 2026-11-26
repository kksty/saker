package host

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/saker/internal/aop"
	"github.com/kksty/saker/internal/testutil"
)

// fakeHarness serves ConfigService/UpdateConfig and AgentService/ListAgents the
// way the real harness does (Connect JSON over HTTP/1.1).
func fakeHarness(t *testing.T, updateStatus int, updateBody string, statuses func(call int32) string) *httptest.Server {
	t.Helper()
	var calls int32
	return testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer host-token" {
			t.Errorf("Authorization = %q", got)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/UpdateConfig"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("UpdateConfig request: %v", err)
			}
			if _, ok := body["config"].(map[string]any); !ok {
				t.Errorf("UpdateConfig body = %v", body)
			}
			w.WriteHeader(updateStatus)
			_, _ = w.Write([]byte(updateBody))
		case strings.HasSuffix(r.URL.Path, "/ListAgents"):
			n := atomic.AddInt32(&calls, 1)
			_, _ = w.Write([]byte(statuses(n)))
		default:
			http.NotFound(w, r)
		}
	}))
}

func agentsJSON(provider, model string) string {
	return `{"agents":[{"hello":{"nodeId":"local"},"status":{"provider":"` + provider + `","model":"` + model + `","bound":true}}]}`
}

func fastHotApplyPoll(t *testing.T) {
	t.Helper()
	previous := hotApplyPoll
	hotApplyPoll = time.Millisecond
	t.Cleanup(func() { hotApplyPoll = previous })
}

func TestPushRuntimeConfigWaitsForAgentBinding(t *testing.T) {
	fastHotApplyPoll(t)
	srv := fakeHarness(t, http.StatusOK, `{"config":{"loaded":true}}`, func(call int32) string {
		if call == 1 {
			return agentsJSON("openai", "old-model")
		}
		return agentsJSON("openai", "new-model")
	})
	defer srv.Close()
	config := map[string]any{"llm": map[string]any{"providers": []any{map[string]any{"model": "new-model"}}}}
	if err := PushRuntimeConfig(context.Background(), srv.URL, "host-token", config, "local", "openai", "new-model", 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPushRuntimeConfigReportsHostRejection(t *testing.T) {
	fastHotApplyPoll(t)
	srv := fakeHarness(t, http.StatusBadRequest, `{"code":"invalid_argument","message":"model is required"}`, func(int32) string {
		return agentsJSON("openai", "old")
	})
	defer srv.Close()
	err := PushRuntimeConfig(context.Background(), srv.URL, "host-token", map[string]any{}, "local", "openai", "new", 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "拒绝") || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("rejection = %v", err)
	}
}

func TestPushRuntimeConfigTimesOutWithLastBinding(t *testing.T) {
	fastHotApplyPoll(t)
	srv := fakeHarness(t, http.StatusOK, `{"config":{"loaded":true}}`, func(int32) string {
		return agentsJSON("openai", "old")
	})
	defer srv.Close()
	err := PushRuntimeConfig(context.Background(), srv.URL, "host-token", map[string]any{}, "local", "openai", "wanted", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "超时") || !strings.Contains(err.Error(), "openai/old") {
		t.Fatalf("timeout = %v", err)
	}
}

// The hot path must not restart the host: StageConfig updates the values a
// future launch reads, and the live child keeps running.
func TestStageConfigUpdatesPendingLaunchWithoutRestart(t *testing.T) {
	h := &Host{}
	h.StageConfig([]string{"CYBER_MODEL=new"}, []string{"--config", "hub.yaml"}, []string{"--config", "new.yaml"})
	if got := h.cfg.Env; len(got) != 1 || got[0] != "CYBER_MODEL=new" {
		t.Fatalf("env = %v", got)
	}
	if got := h.cfg.Args; len(got) != 2 || got[1] != "new.yaml" {
		t.Fatalf("args = %v", got)
	}
	if h.cmd != nil || h.running {
		t.Fatal("StageConfig started or touched a child process")
	}
}

func TestBindingMatchesOnlyEnforcesAskedFields(t *testing.T) {
	if !bindingMatches("anything", "") {
		t.Fatal("empty want must not be enforced (provider may be inferred by the host)")
	}
	if !bindingMatches("OpenAI", "openai") {
		t.Fatal("binding comparison should ignore case")
	}
	if bindingMatches("other", "openai") {
		t.Fatal("a different binding matched")
	}
}

// AOF helper: ensure the fake server's shape stays the same one aop.Agents
// decodes (the host package uses the shared client, not a second decoder).
func TestFakeHarnessShapeIsReadableByAOP(t *testing.T) {
	srv := fakeHarness(t, http.StatusOK, `{}`, func(int32) string { return agentsJSON("p", "m") })
	defer srv.Close()
	agents, err := aop.New(srv.URL, "host-token").Agents(context.Background())
	if err != nil || len(agents) != 1 || agents[0].ID != "local" || agents[0].Model != "m" {
		t.Fatalf("agents = %+v err = %v", agents, err)
	}
}
