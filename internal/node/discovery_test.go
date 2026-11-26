package node

import (
	"context"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/testutil"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeHubDiscoversScanPinsResumeAndRetainsOfflineIdentity(t *testing.T) {
	var offline atomic.Bool
	server := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cyber.rpc.agent.AgentService/ListAgents" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if offline.Load() {
			_, _ = w.Write([]byte(`{"agents":[{"hello":{"nodeId":"native-local","name":"local"},"status":{"bound":true}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"agents":[{"hello":{"nodeId":"native-local","name":"local"},"status":{"bound":true}},{"hello":{"nodeId":"native-worker","name":"worker","runtime":{"workingDir":"shared"}},"status":{"bound":true,"model":"fixture-model"}}]}`))
	}))
	defer server.Close()
	registry, err := New([]Spec{{ID: "local", AOP: server.URL, Agent: "native-local", Token: "fixture-token"}}, "local")
	if err != nil {
		t.Fatal(err)
	}
	registry.SetLocalID("local")
	if err = registry.RefreshNodes(context.Background()); err != nil {
		t.Fatal(err)
	}
	infos := registry.Infos()
	if len(infos) != 2 || infos[1].ID != "scan_native-worker" || infos[1].Ready == nil || !*infos[1].Ready {
		t.Fatalf("discovery: %+v", infos)
	}
	router := registry.Executor(0).(*Router)
	base, token, agent, ok := router.TerminalEndpoint("scan_native-worker")
	if !ok || base != server.URL || token != "fixture-token" || agent != "native-worker" {
		t.Fatal("native node routing lost pin")
	}
	work := t.TempDir()
	_ = os.MkdirAll(filepath.Join(work, ".saker"), 0700)
	_ = os.WriteFile(filepath.Join(work, ".saker", "aop-session.json"), []byte(`{"url":"`+server.URL+`","agent":"native-worker","session":"session"}`), 0600)
	if !router.CanResume("scan_native-worker", work) || router.CanResume("local", work) {
		t.Fatal("session could resume on the wrong scan")
	}
	if _, err = router.Run(context.Background(), executor.Request{Node: "missing"}, func(executor.Event) {}); err == nil {
		t.Fatal("missing node fell back to local")
	}
	offline.Store(true)
	registry.discoveryAt = time.Time{}
	if err = registry.RefreshNodes(context.Background()); err != nil {
		t.Fatal(err)
	}
	infos = registry.Infos()
	if len(infos) != 2 || infos[1].Online == nil || *infos[1].Online {
		t.Fatalf("disconnected identity disappeared: %+v", infos)
	}
}
