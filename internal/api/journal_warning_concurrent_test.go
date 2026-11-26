package api

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
)

// The dedupe of an event's Warning lives inside UpdateRun's mutator. A
// read-then-append outside it would let two events of the same run both see
// "not present" and append the same reason twice, and flushRunWarnings would
// then write two identical journal rows and the console would repeat the banner.
//
// Serial appends cannot tell the two implementations apart (the second one sees
// the first one's write), so concurrency is the input that separates them. This
// is the case the serial test does not cover.
func TestRunWarningDedupeHoldsUnderConcurrentEvents(t *testing.T) {
	s, st := redactTestServer(t)
	const reason = "工具参数不是合法 base64：已按原文记录"
	const workers = 8
	const rounds = 40
	for round := 0; round < rounds; round++ {
		run, err := st.AddRun(model.Run{Node: "local"})
		if err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(s.DataDir, "runs", run.ID+".jsonl")
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := s.openJournal(run.ID, logPath); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if err := s.appendRunEvent(run.ID, executor.Event{
					Kind: executor.KindToolCall, Tool: "bash", Warning: reason,
				}); err != nil {
					errs <- err
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: appendRunEvent: %v", round, err)
		}

		got, ok := st.GetRun(run.ID)
		s.closeJournal(run.ID)
		if !ok {
			t.Fatalf("round %d: run disappeared", round)
		}
		if len(got.Warnings) != 1 {
			t.Fatalf("round %d: %d concurrent events of one reason produced %d warnings: %v",
				round, workers, len(got.Warnings), got.Warnings)
		}
		if got.Warnings[0] != reason {
			t.Fatalf("round %d: warning = %q, want %q", round, got.Warnings[0], reason)
		}
	}
}

// Control for the loop above: distinct reasons under the same concurrency must
// all survive. Without it, an implementation that dropped warnings entirely
// would satisfy "exactly one".
func TestConcurrentDistinctWarningsAllSurvive(t *testing.T) {
	s, st := redactTestServer(t)
	run, err := st.AddRun(model.Run{Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(s.DataDir, "runs", run.ID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.openJournal(run.ID, logPath); err != nil {
		t.Fatal(err)
	}
	defer s.closeJournal(run.ID)

	reasons := []string{"原因甲", "原因乙", "原因丙", "原因丁"}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		r := reasons[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = s.appendRunEvent(run.ID, executor.Event{Kind: executor.KindToolCall, Tool: "bash", Warning: r})
		}()
	}
	close(start)
	wg.Wait()

	got, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	if len(got.Warnings) != len(reasons) {
		t.Fatalf("warnings = %v, want one per distinct reason (%d)", got.Warnings, len(reasons))
	}
	seen := map[string]bool{}
	for _, w := range got.Warnings {
		if seen[w] {
			t.Fatalf("duplicate warning %q in %v", w, got.Warnings)
		}
		seen[w] = true
	}
	for _, r := range reasons {
		if !seen[r] {
			t.Fatalf("reason %q lost: %v", r, got.Warnings)
		}
	}
}
