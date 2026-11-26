package api

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// One bound, one rule: every warning that reaches a run row goes through
// appendRunWarning. These cases pin the three properties that used to differ
// between call sites — dedupe by text, the shared cap, and surviving concurrency.

// Before the unification the API's cap lived only on two of the eight writers
// (model stall, context slim retry); this path had none, so 21 distinct reasons
// produced 21 warnings.
func TestRunWarningCapKeepsTheFirstTwenty(t *testing.T) {
	s, st := redactTestServer(t)
	run, err := st.AddRun(model.Run{Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	reasons := make([]string, 0, model.MaxRunWarnings+3)
	for i := 0; i < model.MaxRunWarnings+3; i++ {
		reasons = append(reasons, fmt.Sprintf("原因-%02d", i))
		s.addRunWarning(run.ID, reasons[i])
	}
	got, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	if len(got.Warnings) != model.MaxRunWarnings {
		t.Fatalf("%d distinct reasons produced %d warnings, want the cap %d",
			len(reasons), len(got.Warnings), model.MaxRunWarnings)
	}
	for i := 0; i < model.MaxRunWarnings; i++ {
		if got.Warnings[i] != reasons[i] {
			t.Fatalf("warning %d = %q, want the first-come text %q", i, got.Warnings[i], reasons[i])
		}
	}
	for _, extra := range reasons[model.MaxRunWarnings:] {
		for _, w := range got.Warnings {
			if w == extra && w != "" {
				t.Fatalf("over-cap reason %q was kept", extra)
			}
		}
	}
}

// The cap is one number in one place (model.MaxRunWarnings). The ledger's
// handoff merge uses the same constant (internal/ledger TestRunWarningsAreCapped
// and TestMergeWarningsDedupesAndBounds), so this test is the other half of the
// equality: if someone restates the number here, a deliberate edit of the model
// bound turns this red instead of drifting silently.
func TestRunWarningCapIsTheSharedModelBound(t *testing.T) {
	if model.MaxRunWarnings != 20 {
		t.Fatalf("model.MaxRunWarnings = %d，改动这个上限要一起看 API 与账本两侧的用例", model.MaxRunWarnings)
	}
	s, st := redactTestServer(t)
	run, err := st.AddRun(model.Run{Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < model.MaxRunWarnings*2; i++ {
		s.addRunWarning(run.ID, fmt.Sprintf("w-%03d", i))
	}
	got, _ := st.GetRun(run.ID)
	if len(got.Warnings) != model.MaxRunWarnings {
		t.Fatalf("warnings = %d, want exactly model.MaxRunWarnings (%d)", len(got.Warnings), model.MaxRunWarnings)
	}
}

// A journal that cannot be written reports the same failure on every event; the
// row must carry it once. Before the unification this writer appended unconditionally,
// so the same text appeared once per failed event and flushRunWarnings then wrote
// that many identical journal rows.
func TestLogWriteFailureIsRecordedOnce(t *testing.T) {
	s, st := redactTestServer(t)
	run, err := st.AddRun(model.Run{Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		s.noteRunLogFailure(run.ID, errors.New("磁盘已满"))
	}
	got, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("50 identical write failures produced %d warnings: %v", len(got.Warnings), got.Warnings)
	}
	if want := "无法写运行事件日志：磁盘已满"; got.Warnings[0] != want {
		t.Fatalf("warning = %q, want %q", got.Warnings[0], want)
	}
}

// The cap check lives inside the mutator, so concurrent appends cannot each pass
// a stale "there is room" test and overrun it.
func TestRunWarningCapHoldsUnderConcurrency(t *testing.T) {
	s, st := redactTestServer(t)
	run, err := st.AddRun(model.Run{Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 30
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		text := fmt.Sprintf("并发原因-%02d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s.addRunWarning(run.ID, text)
		}()
	}
	close(start)
	wg.Wait()
	got, _ := st.GetRun(run.ID)
	if len(got.Warnings) != model.MaxRunWarnings {
		t.Fatalf("%d concurrent reasons produced %d warnings, want the cap %d",
			workers, len(got.Warnings), model.MaxRunWarnings)
	}
	seen := map[string]bool{}
	for _, w := range got.Warnings {
		if seen[w] {
			t.Fatalf("duplicate warning %q in %v", w, got.Warnings)
		}
		seen[w] = true
	}
}
