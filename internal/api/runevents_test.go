package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/store"
	"github.com/kksty/saker/internal/testutil"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A run id is one path segment. %2F is decoded before PathValue, so
// `/api/runs/..%2Fsecret/events` used to resolve outside data/runs and return
// any *.jsonl on the machine.
func TestRunEventsRefusesPathTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := `{"kind":"message","role":"assistant","text":"OUTSIDE"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "secret.jsonl"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{DataDir: dir}
	srv := testutil.NewServer(t, s.eventsMux())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/runs/..%2Fsecret/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", res.StatusCode, body)
	}
	if strings.Contains(string(body), "OUTSIDE") {
		t.Fatalf("the handler served a file outside data/runs: %s", body)
	}
}

func TestRunEventsServesARealRunJournal(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	journal := currentJournalLine(t, `{"kind":"tool_call","tool":"bash"}`) + "\n" + currentJournalLine(t, `{"kind":"turn_ended","stop_reason":"completed"}`) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "runs", "r_abc.jsonl"), []byte(journal), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{DataDir: dir}
	srv := testutil.NewServer(t, s.eventsMux())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/runs/r_abc/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), "turn_ended") || !strings.Contains(string(body), "tool_call") {
		t.Fatalf("journal not returned: %s", body)
	}
}

// eventsMux is the one route the test needs, registered the way Register does.
func (s *Server) eventsMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runs/{id}/events", s.runEvents)
	return mux
}

// A review credential carries no identity and no intent, so it never entered the
// harnessSession block and could read a report/verifier run's journal — the
// previous reviewer's reasoning, which is what makes an independent recomputation
// turn into agreement. The reviewer surface gets the support-run 404 and only
// that: the intent narrowing stays session-only.
func TestReviewerSurfaceCannotReadSupportRunEvents(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	support, err := st.AddRun(model.Run{Purpose: "verifier", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	normal, err := st.AddRun(model.Run{Purpose: "commander", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{support.ID, normal.ID} {
		journal := currentJournalLine(t, `{"kind":"turn_ended","stop_reason":"completed"}`) + "\n"
		if err := os.WriteFile(filepath.Join(dir, "runs", id+".jsonl"), []byte(journal), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{DataDir: dir, Store: st}
	do := func(ctx context.Context, id string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/runs/"+id+"/events", nil)
		req.SetPathValue("id", id)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		s.eventsMux().ServeHTTP(rec, req)
		return rec
	}

	reviewer := WithReviewerSurface(context.Background())
	if rec := do(reviewer, support.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("reviewer read of a verifier run = %d %s, want 404", rec.Code, rec.Body.String())
	}
	if rec := do(reviewer, normal.ID); rec.Code != http.StatusOK {
		t.Fatalf("reviewer read of a normal run = %d %s, want 200", rec.Code, rec.Body.String())
	}
	// Positive control: the same support run is readable by the operator, so the
	// 404 above comes from the marker and not from a missing journal.
	if rec := do(context.Background(), support.ID); rec.Code != http.StatusOK {
		t.Fatalf("operator read of the same verifier run = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// The tail is a ring: once more events exist than the window holds, the answer
// must be the newest ones in file order. A plain array slice would answer with
// the first N or with a wrapped, out-of-order window.
func TestRunEventWindowWrapsInFileOrder(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&log, `{"v":%d,"kind":"tool_call","text":"%d"}`+"\n", executor.JournalSchemaVersion, i)
	}
	// Trailing blank lines are not events and must not consume a slot.
	log.WriteString("\n\n")
	if err := os.WriteFile(filepath.Join(dir, "runs", "r_ring.jsonl"), []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	events, total, err := readRunEventTail(dir, "r_ring", 3)
	if err != nil || total != 10 || len(events) != 3 {
		t.Fatalf("window: total=%d events=%d err=%v", total, len(events), err)
	}
	var texts []string
	for _, raw := range events {
		var event struct{ Text string }
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		texts = append(texts, event.Text)
	}
	if got := strings.Join(texts, ","); got != "7,8,9" {
		t.Fatalf("tail order = %s, want 7,8,9", got)
	}
}

func TestRunEventsRetainsSubmittedInputOutsideTail(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0700); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	log.WriteString(currentJournalLine(t, `{"kind":"input","text":"submitted brief"}`) + "\n")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&log, `{"v":%d,"kind":"tool_call","text":"%d"}`+"\n", executor.JournalSchemaVersion, i)
	}
	log.WriteString(currentJournalLine(t, `{"kind":"prompt","text":"continued turn"}`) + "\n")
	if err := os.WriteFile(filepath.Join(dir, "runs", "r_shortcode2.jsonl"), []byte(log.String()), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{DataDir: dir}
	req := httptest.NewRequest("GET", "/api/runs/r_shortcode2/events?limit=5&include_input=true", nil)
	rec := httptest.NewRecorder()
	s.eventsMux().ServeHTTP(rec, req)
	var body struct {
		Events []struct{ Kind, Text string }
		Total  int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(body.Events) != 6 || body.Total != 502 || body.Events[0].Text != "submitted brief" || body.Events[5].Text != "continued turn" {
		t.Fatalf("input window: %d %s", rec.Code, rec.Body.String())
	}
	events, total, err := readRunEventTail(dir, "r_shortcode2", 5)
	if err != nil || total != 502 || len(events) != 5 {
		t.Fatalf("agent tail changed: %d %d %v", len(events), total, err)
	}
}
