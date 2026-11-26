package metrics

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/executor"
)

func usage(session string, input, cached, ctx int) executor.Event {
	return executor.Event{
		Kind: executor.KindUsage, SessionID: session,
		InputTokens: input, OutputTokens: 10, CachedInputTokens: cached,
		ContextTokens: ctx, ContextWindow: 1_000_000, UsageRequests: 1,
	}
}

func TestBoundaryClassification(t *testing.T) {
	r := Open(t.TempDir())
	defer r.Close()
	if r == nil {
		t.Fatal("recorder should open in a temp dir")
	}
	// A new session's first request is the cold boundary.
	r.Observe("r1", "commander", "", usage("s1", 1000, 0, 1000))
	// The second request in the same run is warm.
	r.Observe("r1", "commander", "", usage("s1", 1200, 1000, 1200))
	// A compaction state marks the next request as the post-compaction boundary.
	r.Observe("r1", "commander", "", executor.Event{Kind: executor.KindStatus, State: "compacted"})
	r.Observe("r1", "commander", "", usage("s1", 300, 200, 300))
	// A second run on the same session is a warm resume, not a cold load.
	r.Observe("r2", "commander", "", usage("s1", 400, 300, 400))
	// A different session is cold again.
	r.Observe("r3", "executor", "in_1", usage("s2", 500, 0, 500))

	sum := r.Summary()
	if sum.Requests != 5 {
		t.Fatalf("requests = %d", sum.Requests)
	}
	if sum.ByClass[ClassSessionFirst] != 2 {
		t.Fatalf("session_first = %d, want 2", sum.ByClass[ClassSessionFirst])
	}
	if sum.ByClass[ClassPostCompaction] != 1 {
		t.Fatalf("post_compaction = %d, want 1", sum.ByClass[ClassPostCompaction])
	}
	if sum.ByClass[ClassWarmResume] != 1 {
		t.Fatalf("warm_resume = %d, want 1", sum.ByClass[ClassWarmResume])
	}
	if sum.ByClass[ClassWarm] != 1 {
		t.Fatalf("warm = %d, want 1", sum.ByClass[ClassWarm])
	}
	if sum.ByPurpose["commander"] != 4 || sum.ByPurpose["executor"] != 1 {
		t.Fatalf("purpose counts = %v", sum.ByPurpose)
	}
}

// A recorder opened after a restart must not label a session that already has
// durable run rows as session_first: the harness session is a warm resume.
func TestSeedSessionsMakesTheFirstPostRestartRequestWarm(t *testing.T) {
	r := Open(t.TempDir())
	defer r.Close()
	r.SeedSessions("s-warm")
	r.Observe("r-new", "commander", "", usage("s-warm", 1000, 800, 1000))
	sum := r.Summary()
	if sum.ByClass[ClassSessionFirst] != 0 {
		t.Fatalf("seeded session was classified session_first: %v", sum.ByClass)
	}
	if sum.ByClass[ClassWarmResume] != 1 {
		t.Fatalf("seeded session class = %v, want one warm_resume", sum.ByClass)
	}
}

func TestCancelVocabularyKeepsRawStrings(t *testing.T) {
	cases := map[[2]string]string{
		{"restart", "进程重启"}:           CancelShutdownRestart,
		{"round_timeout", "本轮超墙钟"}:    CancelRoundTimeout,
		{"model_stall", "空回复"}:        CancelModelStall,
		{"pause", "项目暂停"}:             CancelProjectStop,
		{"project_stop", "删除项目"}:      CancelProjectStop,
		{"stop_loss", "止损"}:           CancelProjectStop,
		{"operator", "人工取消"}:          CancelOperatorPause,
		{"operator_wait", "等待操作员"}:    CancelOperatorPause,
		{"watchdog", "run 无事件 30m"}:   CancelIdleTimeout,
		{"unknown_cause", "whatever"}: CancelOther,
	}
	for in, want := range cases {
		if got := ClassifyCancel(in[0], in[1]); got != want {
			t.Fatalf("ClassifyCancel(%q,%q) = %q, want %q", in[0], in[1], got, want)
		}
	}

	r := Open(t.TempDir())
	defer r.Close()
	r.Cancel("r1", "restart", "进程重启", "canceled")
	sum := r.Summary()
	if sum.Cancels[CancelShutdownRestart] != 1 {
		t.Fatalf("cancels = %v", sum.Cancels)
	}
}

func TestWakeHistogram(t *testing.T) {
	r := Open(t.TempDir())
	defer r.Close()
	r.Wake([]string{"heartbeat"}, true, 100)
	r.Wake([]string{"fact_notice", "goal"}, false, 120)
	r.Wake([]string{"fact_notice"}, false, 110)
	sum := r.Summary()
	if sum.Wakes != 3 || sum.Heartbeats != 1 {
		t.Fatalf("wakes=%d heartbeats=%d", sum.Wakes, sum.Heartbeats)
	}
	if sum.WakeReasons["fact_notice"] != 2 || sum.WakeReasons["heartbeat"] != 1 {
		t.Fatalf("reason histogram = %v", sum.WakeReasons)
	}
}

// A nil recorder must be usable everywhere: a project without a writable data
// directory still runs, it just records nothing.
func TestNilRecorderIsNoop(t *testing.T) {
	var r *Recorder
	r.Observe("r", "commander", "", usage("s", 1, 0, 1))
	r.Wake([]string{"heartbeat"}, true, 0)
	r.Cancel("r", "operator", "", "")
	r.Close()
	if sum := r.Summary(); sum.Requests != 0 {
		t.Fatalf("summary on nil recorder = %+v", sum)
	}
	if r.Path() != "" {
		t.Fatalf("path on nil recorder = %q", r.Path())
	}
}

func TestOpenInUnwritableDirIsNil(t *testing.T) {
	// A file where the metrics directory should be makes MkdirAll fail.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "metrics")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Skipf("cannot create blocker: %v", err)
	}
	if r := Open(dir); r != nil {
		t.Fatalf("Open should fail when metrics path is a file")
	}
}
