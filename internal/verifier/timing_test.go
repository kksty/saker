package verifier

import (
	"context"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
)

func TestReviewQueueAndExecutionHaveSeparateMeasurements(t *testing.T) {
	st, fact, root := setup(t)
	blocked, release := make(chan struct{}), make(chan struct{})
	v := New(st, &stubExec{body: `{"verdict":"pass","notes":"fixture"}`}, root)
	v.Timeout = 500 * time.Millisecond
	v.Admit = func(ctx context.Context) (func(), error) { close(blocked); <-release; return func() {}, ctx.Err() }
	done := make(chan error, 1)
	go func() { _, err := v.Run(context.Background(), fact.ID); done <- err }()
	<-blocked
	var run model.Run
	for _, candidate := range st.Runs() {
		if candidate.Purpose == "verifier" {
			run = candidate
		}
	}
	if run.QueuedAt == nil || run.ExecutionStartedAt != nil || run.ExecutionMS != nil {
		t.Fatalf("queued run pretended to execute: %+v", run)
	}
	time.Sleep(550 * time.Millisecond)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	run, _ = st.GetRun(run.ID)
	if run.ExecutionStartedAt == nil || run.QueueMS == nil || *run.QueueMS < 30 || run.ExecutionMS == nil {
		t.Fatalf("missing separate timing: %+v", run)
	}
	if want := run.EndedAt.Sub(*run.ExecutionStartedAt).Milliseconds(); *run.ExecutionMS != want {
		t.Fatalf("execution included queue: got %d want %d", *run.ExecutionMS, want)
	}
}

func TestReviewCanceledInQueueDoesNotRecordExecutionOrVerdict(t *testing.T) {
	st, fact, root := setup(t)
	queued := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v := New(st, &stubExec{body: `{"verdict":"pass"}`}, root)
	v.Admit = func(ctx context.Context) (func(), error) { close(queued); <-ctx.Done(); return nil, ctx.Err() }
	v.Track = func(string, context.CancelFunc) func() string {
		return func() string { return model.CancelCauseReviewPause }
	}
	done := make(chan error, 1)
	go func() { _, err := v.Run(ctx, fact.ID); done <- err }()
	<-queued
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled queued review reported success")
	}
	for _, run := range st.Runs() {
		if run.Purpose != "verifier" {
			continue
		}
		if run.QueueMS == nil || run.ExecutionStartedAt != nil || run.ExecutionMS != nil || run.Status != model.RunCanceled {
			t.Fatalf("queued cancel recorded execution: %+v", run)
		}
	}
	if len(st.Snapshot().Reviews) != 0 {
		t.Fatal("queued cancel wrote a verdict")
	}
}
