package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
)

// An anomaly named on an event must reach the run's warning banner once, not
// once per event: one malformed payload can recur across many tool calls, and a
// banner that repeats it is noise the operator learns to ignore.
func TestRunWarningDedupesByReason(t *testing.T) {
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
	const reason = "工具参数不是合法 base64：已按原文记录"
	for i := 0; i < 3; i++ {
		if err := s.appendRunEvent(run.ID, executor.Event{
			Kind: executor.KindToolCall, Tool: "bash", Warning: reason,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.appendRunEvent(run.ID, executor.Event{
		Kind: executor.KindToolCall, Tool: "bash", Warning: "工具参数不是 EncodedValue：已按原文记录",
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	if len(got.Warnings) != 2 {
		t.Fatalf("warnings = %v, want one line per distinct reason (2)", got.Warnings)
	}
	if got.Warnings[0] != reason {
		t.Fatalf("first warning = %q, want %q", got.Warnings[0], reason)
	}
}
