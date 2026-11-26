package api

// Controlled end-to-end for the malformed-payload warning chain (round 3).
//
// A fake harness delivers native tool_call lines whose arguments are not valid
// base64. Each line is converted by the SAME production converter the real
// process/AOP executor uses (executor.ParseLine -> PlainToolArguments), then
// handed to the real Server exactly as the dispatch callback does. From there it
// goes through the real appendRunEvent dedupe into the run row, and at settle the
// real flushRunWarnings writes the run's warnings into the journal as
// kind:"warning" lines.
//
// What this pins, that no unit test did: an operator looking at /api/runs (the
// console banner) and at /api/runs/{id}/events (the journal) sees the SAME
// malformed-payload reason, and the reason is recorded once even though the bad
// payload recurred. That is the invariant parity's D section checks: every
// journal warning reason appears exactly once in run.Warnings. Before this the
// live corpus had zero runs with a journal warning line, so D was INSUFFICIENT;
// this run gives it a first real sample.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
)

type badArgsExec struct{}

func (badArgsExec) Name() string { return "bad-args-harness" }

func (badArgsExec) Run(_ context.Context, _ executor.Request, emit func(executor.Event)) (executor.Result, error) {
	// The same malformed call twice: one bad payload that recurs across calls must
	// still leave ONE warning row and ONE journal warning line.
	const line = `{"tool_call":{"id":"call_bad1","name":"bash","arguments":{"data":"!!!not-base64!!!","media_type":"text/plain"}}}`
	emit(executor.ParseLine(line))
	emit(executor.ParseLine(line))
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

// readJournalWarningReasons returns the text of every kind:"warning" line in the
// run's journal, failing if any line is not valid JSON.
func readJournalWarningReasons(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var reasons []string
	for _, ln := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var ev struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(ln), &ev); err != nil {
			t.Fatalf("journal line is not JSON: %s", ln)
		}
		if ev.Kind == "warning" {
			reasons = append(reasons, ev.Text)
		}
	}
	return reasons
}

func TestBadBase64ToolArgumentsReachRunWarningsAndAPI(t *testing.T) {
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	s.Exec = badArgsExec{}

	in, err := p.Store.CreateIntent(model.Intent{Title: "坏 base64 受控 e2e"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, runID)

	run, ok := p.Store.GetRun(runID)
	if !ok {
		t.Fatal("run disappeared")
	}
	joined := strings.Join(run.Warnings, "\n")
	if !strings.Contains(joined, "base64") {
		t.Fatalf("run.Warnings does not name the base64 failure: %v", run.Warnings)
	}
	if n := strings.Count(joined, "base64"); n != 1 {
		t.Fatalf("base64 reason recorded %d times, want 1 (dedupe): %v", n, run.Warnings)
	}

	// /api/runs carries the same warning to the operator's banner.
	runs := call(t, s, http.MethodGet, "/api/runs", nil)
	if runs.Code != http.StatusOK || !strings.Contains(runs.Body.String(), `"warnings"`) || !strings.Contains(runs.Body.String(), "base64") {
		t.Fatalf("/api/runs = %d %s", runs.Code, runs.Body.String())
	}

	// The journal gained a kind:"warning" line at settle and the versioned reader
	// returns it (so the console log shows the same problem).
	events := call(t, s, http.MethodGet, "/api/runs/"+runID+"/events", nil)
	if events.Code != http.StatusOK || !strings.Contains(events.Body.String(), `"kind":"warning"`) || !strings.Contains(events.Body.String(), "base64") {
		t.Fatalf("run events = %d %s", events.Code, events.Body.String())
	}

	// The D-section predicate on the real files: exactly one journal warning line
	// for the one distinct reason, and that reason appears exactly once in the run
	// row. The repeated payload must not add a second line.
	reasons := readJournalWarningReasons(t, filepath.Join(s.DataDir, "runs", runID+".jsonl"))
	if len(reasons) != 1 {
		t.Fatalf("journal warning lines = %d, want 1 (repeated payload deduped): %v", len(reasons), reasons)
	}
	if got := strings.Count(joined, reasons[0]); got != 1 {
		t.Fatalf("journal warning reason appears %d times in run.Warnings, want exactly 1: %v", got, run.Warnings)
	}
	// Evidence for the parity D section: this is a real run whose journal carries
	// exactly one warning line, whose reason appears exactly once in run.Warnings.
	t.Logf("D-sample run=%s", runID)
	t.Logf("journal warning line reason=%q", reasons[0])
	t.Logf("run.Warnings=%q", run.Warnings)
	t.Logf("D predicate PASS: journal_warning_lines=%d, reason_in_run_warnings=%d", len(reasons), strings.Count(joined, reasons[0]))
}
