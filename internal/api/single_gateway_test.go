package api

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
)

type rejectedGateway struct{ calls atomic.Int32 }

func (e *rejectedGateway) Name() string { return "gateway" }
func (e *rejectedGateway) Run(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
	e.calls.Add(1)
	return executor.Result{ExitCode: 1, IsError: true, Output: "API error (401): invalid_api_key", StopReason: "error"}, nil
}

func TestGatewayRejectionPausesWithoutRotatingOrReplaying(t *testing.T) {
	f := newWorkerFixture(t)
	ex := &rejectedGateway{}
	f.srv.Exec = ex
	f.srv.ModelSettings = func() (string, string, int) { return "openai", "gateway", 64000 }
	// Old bindings are preserved in the ledger but cannot select a new route.
	in, err := f.p.Store.CreateIntent(model.Intent{Title: "legacy intent", Profile: "retired"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := f.srv.startRun(in.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !f.srv.WaitIdle(3 * time.Second) {
		t.Fatal("failed gateway run did not settle")
	}
	run, ok := f.p.Store.GetRun(runID)
	if !ok || run.Status != model.RunFailed || run.Model != "gateway" || run.ResolvedProfile != "" {
		t.Fatalf("gateway run = %+v", run)
	}
	if !f.srv.paused.Load() || ex.calls.Load() != 1 || len(f.p.Store.Runs()) != 1 {
		t.Fatalf("rejection did not pause once: paused=%v calls=%d runs=%d", f.srv.paused.Load(), ex.calls.Load(), len(f.p.Store.Runs()))
	}
	if _, err := f.srv.startRun(in.ID, "", ""); err == nil || ex.calls.Load() != 1 {
		t.Fatal("rejected gateway was replayed while paused")
	}
}
