package api

import (
	"context"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/host"
)

type modelExecStub struct {
	runs int
	last executor.Request
}

func (e *modelExecStub) Name() string { return "stub" }

func (e *modelExecStub) Run(_ context.Context, req executor.Request, _ func(executor.Event)) (executor.Result, error) {
	e.runs++
	e.last = req
	return executor.Result{ExitCode: 0}, nil
}

type modelExecAware struct {
	*modelExecStub
	nodes   []executor.NodeInfo
	resolve func(string) string
}

func (e *modelExecAware) Nodes() []executor.NodeInfo { return e.nodes }

func (e *modelExecAware) ResolveNode(requested string) string { return e.resolve(requested) }

// nodeAwareExecutor is what DefaultModelExecutor's wrapper implements, so the
// node surface can be asserted without reaching into the unexported type.
type nodeAwareExecutor interface {
	executor.Executor
	Nodes() []executor.NodeInfo
	ResolveNode(string) string
}

func unreadyServer() *Server {
	return &Server{LocalNode: "local", LocalHostStatus: func() host.HostStatus {
		return host.HostStatus{Online: true, Ready: false, LastError: "API error (402): Insufficient Balance"}
	}}
}

// The wrapper is the auxiliary-call equivalent of the dispatch readiness gate:
// a run resolved to the local host must be refused before the executor is
// called, and a remote node must be left to its own host.
func TestDefaultModelExecutorGatesRunsOnLocalHostReadiness(t *testing.T) {
	s := unreadyServer()
	ex := &modelExecStub{}
	if _, err := s.DefaultModelExecutor(ex).Run(context.Background(), executor.Request{Node: "local"}, nil); err == nil || !strings.Contains(err.Error(), "本机宿主未就绪") {
		t.Fatalf("local run was not gated: %v", err)
	}
	if ex.runs != 0 {
		t.Fatalf("executor ran despite an unready host: %d", ex.runs)
	}
}

func TestDefaultModelExecutorLeavesRemoteRunsToTheNode(t *testing.T) {
	s := unreadyServer()
	ex := &modelExecStub{}
	if _, err := s.DefaultModelExecutor(ex).Run(context.Background(), executor.Request{Node: "lab"}, nil); err != nil {
		t.Fatalf("remote run: %v", err)
	}
	if ex.runs != 1 {
		t.Fatalf("remote run count = %d, want 1", ex.runs)
	}
}

func TestDefaultModelExecutorDelegatesNodeSurface(t *testing.T) {
	s := &Server{LocalNode: "local"}
	plain := (&Server{LocalNode: "local"}).DefaultModelExecutor(&modelExecStub{}).(nodeAwareExecutor)
	if got := plain.Nodes(); len(got) != 1 || got[0].ID != "local" || !got[0].Default {
		t.Fatalf("plain nodes = %+v", got)
	}
	if got := plain.ResolveNode("lab"); got != "lab" {
		t.Fatalf("plain resolve = %q, want lab", got)
	}

	ready := true
	aware := &modelExecAware{
		modelExecStub: &modelExecStub{},
		nodes:         []executor.NodeInfo{{ID: "lab", Ready: &ready}},
		resolve:       func(string) string { return "lab" },
	}
	wrapped := s.DefaultModelExecutor(aware).(nodeAwareExecutor)
	if got := wrapped.Nodes(); len(got) != 1 || got[0].ID != "lab" {
		t.Fatalf("aware nodes = %+v", got)
	}
	if got := wrapped.ResolveNode("anything"); got != "lab" {
		t.Fatalf("aware resolve = %q, want lab", got)
	}
}
