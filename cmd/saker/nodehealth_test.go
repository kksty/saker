package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/kksty/saker/internal/node"
)

func TestProbeAOPNodeListsAgents(t *testing.T) {
	var path string
	base := newInProcessHost(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"agents":[{"hello":{"nodeId":"local"}},{"hello":{"nodeId":"lab"}}]}`)
	}))

	health := probeNode(context.Background(), node.Spec{ID: "lab", AOP: base, Token: "tok"})
	if path != "/cyber.rpc.agent.AgentService/ListAgents" {
		t.Fatalf("probe path = %q", path)
	}
	if !health.Online || health.Agents != 2 || health.CheckedAt == nil {
		t.Fatalf("health = %+v", health)
	}
	if health.Error != "" {
		t.Fatalf("error = %q", health.Error)
	}
}

func TestProbeAOPNodeReportsFailure(t *testing.T) {
	health := probeNode(context.Background(), node.Spec{ID: "dead", AOP: "http://127.0.0.1:1", Token: "tok"})
	if health.Online || health.Error == "" || health.CheckedAt == nil {
		t.Fatalf("health = %+v", health)
	}
}

func TestProbeAuditNodeChecksBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	health := probeNode(context.Background(), node.Spec{ID: "audit", Bin: exe, Audit: true})
	if !health.Online || health.Error != "" {
		t.Fatalf("health = %+v", health)
	}

	missing := probeNode(context.Background(), node.Spec{ID: "gone", Bin: exe + ".does-not-exist", Audit: true})
	if missing.Online || missing.Error == "" {
		t.Fatalf("missing bin health = %+v", missing)
	}
}
