package decider

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/model"
)

// The automatic fallback request is only for "goal open + work attempted +
// nothing in flight". A fresh goal or any live direction must not create it.
func TestIdleGoalNeedsOperator(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, nil, "")
	if r.idleGoalNeedsOperator() {
		t.Fatal("a project without a goal is not idle-waiting for the operator")
	}
	if _, err := st.AddGoal(model.Goal{Statement: "open goal"}); err != nil {
		t.Fatal(err)
	}
	if r.idleGoalNeedsOperator() {
		t.Fatal("a fresh goal with no direction is not idle-waiting")
	}
	done, err := st.CreateIntent(model.Intent{Title: "finished work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(done.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if !r.idleGoalNeedsOperator() {
		t.Fatal("an open goal after work is idle-waiting")
	}
	pending, err := st.CreateIntent(model.Intent{Title: "pending work"})
	if err != nil {
		t.Fatal(err)
	}
	if r.idleGoalNeedsOperator() {
		t.Fatal("a pending direction is work, not idle-waiting")
	}
	if _, err := st.UpdateIntent(pending.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if !r.idleGoalNeedsOperator() {
		t.Fatal("the idle state returned after the direction finished")
	}
}

// A failed direction is settled, not work in flight: it needs a decision, not a
// wait. Counting it as live suppressed the operator request exactly when the
// project could not move by itself and said "nothing to handle".
func TestSettledDirectionStillAsksTheOperator(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, nil, "")
	if _, err := st.AddGoal(model.Goal{Statement: "open goal"}); err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{Title: "failed work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusFailed }); err != nil {
		t.Fatal(err)
	}
	if !r.idleGoalNeedsOperator() {
		t.Fatal("an attempted direction that settled must leave the idle decision to the operator")
	}
	r.refreshIdleOperatorRequest()
	if got := len(st.PendingOperatorRequests()); got != 1 {
		t.Fatalf("idle operator request = %d, want 1", got)
	}
}

// The fallback is control state, so it is created without a model round and
// stays single while idle; work resumes and it is dismissed.
func TestRefreshIdleOperatorRequestCreatesAndDismissesFallback(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, nil, "")
	if _, err := st.AddGoal(model.Goal{Statement: "open goal"}); err != nil {
		t.Fatal(err)
	}
	done, err := st.CreateIntent(model.Intent{Title: "finished work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(done.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}

	r.refreshIdleOperatorRequest()
	pending := st.PendingOperatorRequests()
	if len(pending) != 1 || !pending[0].Generic {
		t.Fatalf("fallback = %+v", pending)
	}
	r.refreshIdleOperatorRequest()
	if got := len(st.PendingOperatorRequests()); got != 1 {
		t.Fatalf("fallback duplicated: %d", got)
	}

	if _, err := st.CreateIntent(model.Intent{Title: "new work"}); err != nil {
		t.Fatal(err)
	}
	r.refreshIdleOperatorRequest()
	if got := len(st.PendingOperatorRequests()); got != 0 {
		t.Fatalf("fallback survived resumed work: %d", got)
	}
	if got, _ := st.GetOperatorRequest(pending[0].ID); got.Status != model.OperatorRequestDismissed {
		t.Fatalf("fallback status = %q", got.Status)
	}
}

// The fallback wakes exactly one forced round per idle episode. Heartbeats keep
// calling refresh, but the latch and the pending row keep it from waking again;
// the watchdog grants at most one extra round, and only while the request is
// still generic.
func TestIdleFallbackWakesOnceAndEscalationIsCapped(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, nil, "")
	if _, err := st.AddGoal(model.Goal{Statement: "open goal"}); err != nil {
		t.Fatal(err)
	}
	done, err := st.CreateIntent(model.Intent{Title: "finished work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(done.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}

	r.refreshIdleOperatorRequest()
	if got := len(st.PendingOperatorRequests()); got != 1 {
		t.Fatalf("idle request = %d, want 1", got)
	}
	if got := len(r.wake); got != 1 {
		t.Fatalf("first idle wake = %d, want exactly 1", got)
	}
	<-r.wake
	// A heartbeat refresh must not wake again or add a second request.
	r.refreshIdleOperatorRequest()
	if got := len(r.wake); got != 0 {
		t.Fatalf("repeated idle refresh woke the commander again")
	}
	if got := len(st.PendingOperatorRequests()); got != 1 {
		t.Fatalf("repeated idle refresh duplicated the request: %d", got)
	}

	// Watchdog escalation: after three no-action rounds and while the request is
	// still generic, one more forced round; then the episode is spent.
	r.mu.Lock()
	r.noActionRounds = 3
	r.mu.Unlock()
	r.refreshIdleOperatorRequest()
	if got := len(r.wake); got != 1 {
		t.Fatalf("watchdog wake = %d, want 1", got)
	}
	<-r.wake
	r.mu.Lock()
	r.noActionRounds = 9
	r.mu.Unlock()
	r.refreshIdleOperatorRequest()
	if got := len(r.wake); got != 0 {
		t.Fatalf("watchdog escalated past the cap")
	}
}

// A stalled round may record a specific blocker. Its handoff keeps the proposed
// next step in the note for an incoming commander to check against current state.
func TestIdleReplanSectionStatesTheSituationAndTheRequest(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, nil, "")
	if _, err := st.AddGoal(model.Goal{Statement: "朝着站点权限或 RCE 去打"}); err != nil {
		t.Fatal(err)
	}
	done, err := st.CreateIntent(model.Intent{Title: "finished work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(done.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	r.refreshIdleOperatorRequest()
	<-r.wake

	pending := st.PendingOperatorRequests()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	b := board.Build(st, 20)
	section := r.idleReplanSection(b, false)
	for _, want := range []string{
		"目标未达成，没有在办意图",
		"本轮若确实无可推进",
		"具体的操作员请求",
		"局部止损只关闭那一条",
		"不可达",
		"PUT /api/operator-requests/" + pending[0].ID,
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("idle section missing %q:\n%s", want, section)
		}
	}
	if strings.Contains(section, "本轮必须给出下一步") {
		t.Fatalf("the heading must not order a next step: a direction with nothing to advance may say so:\n%s", section)
	}
	if !strings.Contains(section, "或写明卡点") {
		t.Fatalf("the heading must name the alternative the body allows:\n%s", section)
	}
	handoff := r.idleReplanSection(b, true)
	if !strings.Contains(handoff, "本轮交付交接笔记") || strings.Contains(handoff, "派发或续跑一个在三份项目文档范围内的意图") {
		t.Fatalf("handoff variant must ask for the note, not a dispatch:\n%s", handoff)
	}
	if !strings.Contains(handoff, "下一轮按当前有效指令与账本核对后再判断") || strings.Contains(handoff, "下一轮必须执行") {
		t.Fatalf("handoff must carry advice without committing the next round:\n%s", handoff)
	}
}

// A stale no-action count from before the idle episode must not trigger the
// watchdog round immediately after the first replan. The escalation measures
// the rounds burned *since* the replan.
func TestIdleEscalationMeasuresFromTheReplan(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, nil, "")
	if _, err := st.AddGoal(model.Goal{Statement: "open goal"}); err != nil {
		t.Fatal(err)
	}
	done, err := st.CreateIntent(model.Intent{Title: "finished work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(done.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.noActionRounds = 9
	r.mu.Unlock()

	r.refreshIdleOperatorRequest() // creates the request, replan #1, base = 9
	if got := len(r.wake); got != 1 {
		t.Fatalf("first wake = %d, want 1", got)
	}
	<-r.wake
	r.refreshIdleOperatorRequest() // still 9: no new no-action round
	if got := len(r.wake); got != 0 {
		t.Fatalf("stale count escalated immediately")
	}
	r.mu.Lock()
	r.noActionRounds = 12 // three more no-action rounds since the replan
	r.mu.Unlock()
	r.refreshIdleOperatorRequest()
	if got := len(r.wake); got != 1 {
		t.Fatalf("escalation after three new no-action rounds = %d, want 1", got)
	}
}
