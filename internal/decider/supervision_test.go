package decider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/model"
)

func TestHeartbeatSupervisesNewFailuresAndSilenceOnlyOnce(t *testing.T) {
	st := newStore(t)
	in, err := st.CreateIntent(model.Intent{Title: "live"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpdateIntent(in.ID, func(in *model.Intent) { in.Status, in.RunID = model.StatusRunning, run.ID })
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, nil, "")
	r.ModelName = "fixture"
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		d, _ := st.PendingDelivery("commander")
		return RoundResult{}, st.AcknowledgeDelivery("commander", d.ID)
	}
	if r.Run(context.Background(), "manual", true) == nil {
		t.Fatal("initial round skipped")
	}
	st.NoteEvent(run.ID, time.Now(), "tool_result", "bash", "tool returned")
	if r.heartbeatWorthwhile() {
		t.Fatal("ordinary activity woke commander")
	}
	if err := st.ObserveRun(run.ID, "session", true); err != nil {
		t.Fatal(err)
	}
	if !r.heartbeatWorthwhile() {
		t.Fatal("busy failure was invisible")
	}
	if r.Run(context.Background(), "heartbeat", false) == nil || r.heartbeatWorthwhile() {
		t.Fatal("failure was not supervised exactly once")
	}
	idle := time.Now().Add(-supervisionIdle - time.Second)
	st.NoteEvent(run.ID, idle, "status", "", "waiting")
	if !r.heartbeatWorthwhile() {
		t.Fatal("new silence was invisible")
	}
	if r.Run(context.Background(), "heartbeat", false) == nil || r.heartbeatWorthwhile() {
		t.Fatal("same silence caused repeated rounds")
	}
	st.NoteEvent(run.ID, time.Now(), "tool_result", "bash", "recovered")
	if r.heartbeatWorthwhile() {
		t.Fatal("recovery alone caused a speculative round")
	}
	st.NoteEvent(run.ID, idle.Add(time.Second), "status", "", "waiting again")
	if !r.heartbeatWorthwhile() {
		t.Fatal("second silence episode was invisible")
	}
	_, _ = st.AddRun(model.Run{Purpose: "commander", ToolFailures: 99})
	r.lastSupervision = supervisionMarks(board.Build(st, 20))
	if r.heartbeatWorthwhile() {
		t.Fatal("commander supervised itself")
	}
}

// A round that dies on its way to the model never told the commander anything,
// so it must not consume the supervision marks. Committing them up front meant
// the heartbeat could never raise the same failure again: the one mechanism
// that notices a silent run went quiet after a single transport error.
func TestFailedRoundDoesNotConsumeSupervisionMarks(t *testing.T) {
	st := newStore(t)
	in, err := st.CreateIntent(model.Intent{Title: "live"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateIntent(in.ID, func(in *model.Intent) { in.Status, in.RunID = model.StatusRunning, run.ID }); err != nil {
		t.Fatal(err)
	}
	if err := st.ObserveRun(run.ID, "session", true); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, nil, "")
	r.ModelName = "fixture"
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		return RoundResult{}, errors.New("宿主在派发前重连")
	}
	if !r.heartbeatWorthwhile() {
		t.Fatal("new tool failure was invisible")
	}
	if d := r.Run(context.Background(), "heartbeat", false); d == nil {
		t.Fatal("the round was not attempted")
	}
	if !r.heartbeatWorthwhile() {
		t.Fatal("a failed round consumed the supervision mark; the failure can never wake the commander again")
	}
}

// A round that never reached the model has no opinion about compaction, and it
// certainly was not a handoff attempt: the next real round must still receive
// the full note and the lossy-memory warning.
func TestFailedRoundKeepsCompactionFlagAndHandoffPressure(t *testing.T) {
	st := newStore(t)
	if _, err := st.CreateIntent(model.Intent{Title: "open"}); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(st, nil, "")
	r.ModelName, r.ContextWindow = "fixture", 1000
	r.lastCompacted = true
	r.lastContext = 900 // past the handoff line, so this wake is a pressure round
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		return RoundResult{}, errors.New("网关 401")
	}
	if d := r.Run(context.Background(), "operator", true); d == nil {
		t.Fatal("the round was not attempted")
	}
	if !r.lastCompacted {
		t.Fatal("a round that never reached the model cleared the compaction flag")
	}
	if r.handoffMissing != 0 {
		t.Fatalf("handoffMissing = %d, want 0: a transport failure is not a missing receipt", r.handoffMissing)
	}
}

func TestNewFactsQueueWithoutCancelingActiveCommander(t *testing.T) {
	st := newStore(t)
	_, _ = st.CreateIntent(model.Intent{Title: "open"})
	r := NewRunner(st, nil, "")
	r.ModelName, r.Debounce, r.Heartbeat = "fixture", time.Millisecond, time.Hour
	entered, release, next := make(chan context.Context, 1), make(chan struct{}), make(chan struct{}, 1)
	calls := 0
	r.Commander = func(ctx context.Context, _ string, _ bool) (RoundResult, error) {
		calls++
		if calls == 1 {
			entered <- ctx
			select {
			case <-release:
			case <-ctx.Done():
			}
		} else {
			next <- struct{}{}
		}
		return RoundResult{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	r.Wake("operator")
	var active context.Context
	select {
	case active = <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no active round")
	}
	_, _ = st.AddFact(model.Fact{Title: "new"})
	for range 10 {
		r.Wake("fact")
	}
	if active.Err() != nil {
		t.Fatal("fact interrupted current round")
	}
	close(release)
	select {
	case <-next:
	case <-time.After(2 * time.Second):
		t.Fatal("queued fact did not run")
	}
}

func TestContinuousFactsCannotStarveDebouncedCommander(t *testing.T) {
	r := NewRunner(newStore(t), nil, "")
	r.Debounce = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				r.Wake("fact")
			}
		}
	}()
	done := make(chan bool, 1)
	go func() { reason, ok := r.debounce(ctx, "operator"); done <- ok && reason == "operator" }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("forced wake lost priority")
		}
	case <-time.After(time.Second):
		t.Fatal("continuous facts starved commander")
	}
}
