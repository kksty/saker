package api

import (
	"testing"

	"github.com/kksty/saker/internal/aop"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The provider-text gate must read the link, not the model: a judgement failure
// and a credential rejection are both "failed" runs with no recorded cause, and
// retrying them would spend tokens on an answer that will not change.
func TestTransportFailureOnlyMatchesTheLink(t *testing.T) {
	cases := []struct {
		name string
		out  string
		stop string
		want bool
	}{
		{"reset", "read tcp: connection reset by peer", "", true},
		{"dial", "", "dial tcp 172.28.51.140:4061: i/o timeout", true},
		{"stream chunk", "", "unmarshal stream chunk: unexpected EOF", true},
		{"tool timeout", "媒体保全失败：i/o timeout", "", false},
		{"context deadline", "context deadline exceeded", "", false},
		{"tool exit", "exit status 1: curl: (7) Failed to connect", "", false},
		{"judgement", "本轮没有找到可利用路径", "", false},
		{"credential", "API error (401): invalid api key", "", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := executor.Result{Output: c.out, StopReason: c.stop}
			if got := transportFailure(res); got != c.want {
				t.Fatalf("transportFailure(%q,%q) = %v, want %v", c.out, c.stop, got, c.want)
			}
		})
	}
}

// The recorded death wins over the text: an AOP stream that dropped never puts
// its reason in Result.Output, and a tool-side timeout must not make an
// unrelated failure look like a broken link.
func TestTransportDeathPrefersTheRecordedClass(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/ledger.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	cases := []struct {
		name  string
		class string
		res   executor.Result
		want  bool
	}{
		{"stream died on the link", aop.FailureTransport, executor.Result{}, true},
		{"stream ended with no reason", aop.FailureUnknown, executor.Result{}, true},
		{"host closed on purpose", aop.FailureIntentional, executor.Result{Output: "connection reset by peer"}, false},
		{"no class, provider dial error", "", executor.Result{StopReason: "dial tcp 10.0.0.1:443: connectex: refused"}, true},
		{"no class, tool timeout", "", executor.Result{Output: "媒体保全：i/o timeout"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := st.AddRun(model.Run{IntentID: "in_class"})
			if err != nil {
				t.Fatal(err)
			}
			if c.class != "" {
				if _, err := st.UpdateRun(r.ID, func(run *model.Run) { run.FailureClass = c.class }); err != nil {
					t.Fatal(err)
				}
			}
			if got := s.transportDeath(r.ID, c.res); got != c.want {
				t.Fatalf("transportDeath(class=%q) = %v, want %v", c.class, got, c.want)
			}
		})
	}
}

// A successful run ends the episode: the next link failure starts from attempt
// one instead of inheriting the previous budget.
func TestAutoResumeBudgetResetsAfterSuccess(t *testing.T) {
	resetAutoResumeBudget("in_reset")
	for i := 0; i < maxAutoResumes; i++ {
		autoResumeMark("in_reset")
	}
	if got := autoResumeUsed("in_reset"); got != maxAutoResumes {
		t.Fatalf("budget = %d, want %d", got, maxAutoResumes)
	}
	resetAutoResumeBudget("in_reset")
	if got := autoResumeUsed("in_reset"); got != 0 {
		t.Fatalf("budget after reset = %d, want 0", got)
	}
}

// A timer that never started a run must give its slot back: three lost races
// would otherwise silence the self-heal for that direction.
func TestAutoResumeRefundReturnsAnUnusedSlot(t *testing.T) {
	resetAutoResumeBudget("in_refund")
	t.Cleanup(func() { resetAutoResumeBudget("in_refund") })
	autoResumeMark("in_refund")
	autoResumeMark("in_refund")
	autoResumeRefund("in_refund")
	if got := autoResumeUsed("in_refund"); got != 1 {
		t.Fatalf("budget after refund = %d, want 1", got)
	}
	autoResumeRefund("in_refund")
	autoResumeRefund("in_refund")
	if got := autoResumeUsed("in_refund"); got != 0 {
		t.Fatalf("budget must not go negative, got %d", got)
	}
}

// A failed parent whose children are still running is not restarted: RunFailed
// cleared the parent's wait, so the children's results already have nowhere to
// land, and restarting would put the parent on the same ground as its children.
// A direction already retried by the operator (or the pump) between the timer
// being armed and firing is left alone: that work is already running.
func TestAutoResumeFireLeavesADirectionAlreadyTakenAlone(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/ledger.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	in, err := st.CreateIntent(model.Intent{Title: "已被接管"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusPending }); err != nil {
		t.Fatal(err)
	}
	resetAutoResumeBudget(in.ID)
	t.Cleanup(func() { resetAutoResumeBudget(in.ID) })

	s.autoResumeFire("r_taken", in.ID, nil)

	got, _ := st.GetIntent(in.ID)
	if got.Status != model.StatusPending {
		t.Fatalf("status = %q, want pending: the newer request owns it", got.Status)
	}
	if used := autoResumeUsed(in.ID); used != 0 {
		t.Fatalf("budget = %d, want 0", used)
	}
}
