package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// currentJournalLine stamps the current schema version on one fixture line, so
// a test exercises the format this build reads without hardcoding the number.
func currentJournalLine(t *testing.T, line string) string {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatalf("fixture line is not JSON: %v\n%s", err, line)
	}
	obj["v"] = executor.JournalSchemaVersion
	out, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// stampJournalVersion rewrites a fixture journal so every line carries the
// current version. Tests keep their readable literals; the file is what the
// readers see.
func stampJournalVersion(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// A fixture may carry a deliberately broken line (a test that the
		// reader skips it); the version stamp leaves it exactly as written.
		var obj map[string]any
		if json.Unmarshal([]byte(line), &obj) != nil {
			out.WriteString(line)
			out.WriteString("\n")
			continue
		}
		out.WriteString(currentJournalLine(t, line))
		out.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Four readers, one answer: a journal written by another build is not read by
// any of them, and the same file, once stamped, is read by all of them. Two
// halves so a constant "skip everything" would not pass.
func TestReadersRefuseAnUnversionedJournal(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runs, "r_old.jsonl")
	lines := []string{
		`{"kind":"tool_call","tool":"bash","at":"2026-01-01T00:00:00Z","raw":{"tool_call":{"arguments":"{\"command\":\"curl 10.0.0.5:8080\"}"}}}`,
		`{"kind":"tool_result","tool":"bash","ok":false}`,
		`{"kind":"judge_usage","judge":"jev","total":10}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(nil, nil, dir, filepath.Join(dir, "cases"))

	// Before: nothing reads it.
	if rows, _ := s.toolLeaderboard(); len(rows) != 0 {
		t.Fatalf("leaderboard read an unversioned journal: %+v", rows)
	}
	if _, ok := readProbeJournal(path); ok {
		t.Fatal("probe table read an unversioned journal")
	}
	if scan := s.scanJudgeUsage(); scan.unreadable == 0 || len(scan.unavailable) == 0 {
		t.Fatalf("judge scan accepted an unversioned journal: %+v", scan)
	}
	if _, _, err := readRunEventWindow(dir, "r_old", 10, false); !errors.Is(err, errJournalVersion) {
		t.Fatalf("event window err = %v, want the version refusal", err)
	}

	// After: the same file, written by this build, is read by every reader.
	stampJournalVersion(t, path)
	if rows, _ := s.toolLeaderboard(); len(rows) == 0 || rows[0].Tool != "bash" {
		t.Fatalf("leaderboard did not read the stamped journal: %+v", rows)
	}
	if probes, ok := readProbeJournal(path); !ok || len(probes) != 1 {
		t.Fatalf("probe table = %+v ok=%v, want the one probed address", probes, ok)
	}
	if scan := s.scanJudgeUsage(); scan.unreadable != 0 || scan.rows != 1 {
		t.Fatalf("judge scan = %+v, want one readable row", scan)
	}
	if events, total, err := readRunEventWindow(dir, "r_old", 10, false); err != nil || total != 3 || len(events) != 3 {
		t.Fatalf("event window = %d/%d err=%v, want 3/3", total, len(events), err)
	}
}

// The journal writer has five line shapes. Every one of them must say which
// format it is; a shape nobody stamped would be refused by the reader with no
// clue, and the fix would be guesswork about which field is missing.
func TestEveryJournalLineCarriesTheSchemaVersion(t *testing.T) {
	s, st := redactServerWithSecrets(t, "sk-live-unused-0000000000")
	run, logPath := newJournalRun(t, s, st)

	lines := []any{
		executor.Event{Kind: executor.KindToolResult, Tool: "bash", OK: executor.BoolPtr(true)},
		map[string]any{"kind": "run_started", "at": time.Now()},
		collabEvent{Kind: "collab_message", At: time.Now(), MessageID: "m_1", From: "pt_x", To: "pt_y", Direction: "in", Delivery: "accepted"},
		injectedEvent{Kind: "injected", At: time.Now(), RunID: run.ID, Source: "message", Bytes: 3},
		runWarningEvent{Kind: "warning", At: time.Now(), Text: "w"},
		guardrailJournalEvent{Kind: "guardrail", At: time.Now(), Action: "pending_read", SessionID: "s_1"},
		procPeakEvent{Kind: "proc_peak", At: time.Now()},
	}
	for _, line := range lines {
		if err := s.appendEvent(run.ID, line); err != nil {
			t.Fatalf("append %T: %v", line, err)
		}
	}
	s.closeJournal(run.ID)

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		got++
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %d is not JSON: %v\n%s", got, err, line)
		}
		if v, ok := obj["v"]; !ok || v != float64(executor.JournalSchemaVersion) {
			t.Fatalf("line %d (%s) has no current schema version: %s", got, obj["kind"], line)
		}
	}
	if got != len(lines) {
		t.Fatalf("wrote %d lines, want %d", got, len(lines))
	}
}

// The reader does not negotiate: a journal without the version is from another
// build and the API says so, instead of inferring the shape from missing fields.
func TestToolOutputRefusesAnUnversionedJournal(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	run, err := st.AddRun(model.Run{IntentID: "in_fixture"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, dir, dir)
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"tool_result": map[string]any{"output": []any{map[string]any{"text": map[string]any{"text": "body"}}}}})
	ev := executor.Event{Kind: executor.KindToolResult, CallID: "c_old", Raw: raw}
	body, _ := json.Marshal(ev) // no V: the old writer's line
	path := filepath.Join(dir, "runs", run.ID+".jsonl")
	if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	s.Register(mux)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/api/runs/"+run.ID+"/output?call_id=c_old", nil))
	if rr.Code != 500 || !strings.Contains(rr.Body.String(), "不是当前版本") {
		t.Fatalf("unversioned journal = %d %s, want 500 naming the old format", rr.Code, rr.Body.String())
	}

	// Positive control: the same line, written by this build, is read normally.
	ev.V = executor.JournalSchemaVersion
	body, _ = json.Marshal(ev)
	if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/api/runs/"+run.ID+"/output?call_id=c_old", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "body") {
		t.Fatalf("versioned journal = %d %s, want 200 with the body", rr.Code, rr.Body.String())
	}
}
