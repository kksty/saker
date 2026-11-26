package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

func newOperatorScopeServer(t *testing.T, ex executor.Executor) (*Server, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(50 * time.Millisecond)
	})
	return s, st, dir
}

type scopeCallOption func(*http.Request) *http.Request

func callOperatorScope(t *testing.T, s *Server, method, path string, body any, opts ...scopeCallOption) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.Register(mux)
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	for _, opt := range opts {
		req = opt(req)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func decodeScopeResponse(t *testing.T, w *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
}

func withCommanderIdentity(req *http.Request) *http.Request {
	return req.WithContext(WithIdentity(req.Context(), collab.Identity{
		ProjectID: "p", ParticipantID: collab.CommanderID, RunID: "r_auth_scope",
	}))
}

func withExecutorIdentity(req *http.Request) *http.Request {
	return req.WithContext(WithIdentity(req.Context(), collab.Identity{
		ProjectID: "p", ParticipantID: "in_1", IntentID: "in_1", RunID: "r_auth_scope",
	}))
}

func withAgentSurface(req *http.Request) *http.Request {
	return req.WithContext(WithAgentSurface(req.Context()))
}

func withCommanderActorClaim(req *http.Request) *http.Request {
	req.Header.Set("X-Saker-Actor", collab.CommanderID)
	return req
}

// Every operator-scope surface is operator-only: a harness session that reaches
// the project mux is answered 404 (not 403 — a 404 does not confirm the surface
// exists), and so is a request carrying the commander actor claim.
func TestOperatorScopeOperatorOnly(t *testing.T) {
	s, _, _ := newOperatorScopeServer(t, &blockingExec{})
	body := map[string]any{"kind": "authorization", "summary": "允许新增资产"}
	for _, tc := range []struct {
		name   string
		opt    scopeCallOption
		method string
	}{
		{"commander identity get", withCommanderIdentity, http.MethodGet},
		{"commander identity post", withCommanderIdentity, http.MethodPost},
		{"executor identity get", withExecutorIdentity, http.MethodGet},
		{"executor identity post", withExecutorIdentity, http.MethodPost},
		{"agent surface get", withAgentSurface, http.MethodGet},
		{"agent surface post", withAgentSurface, http.MethodPost},
		{"commander actor claim get", withCommanderActorClaim, http.MethodGet},
		{"commander actor claim post", withCommanderActorClaim, http.MethodPost},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reqBody any
			if tc.method == http.MethodPost {
				reqBody = body
			}
			w := callOperatorScope(t, s, tc.method, "/api/operator-scope", reqBody, tc.opt)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d %s, want 404", w.Code, w.Body.String())
			}
		})
	}
	// The operator credential (no identity, no actor claim) can write and read.
	w := callOperatorScope(t, s, http.MethodPost, "/api/operator-scope", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("operator post = %d %s", w.Code, w.Body.String())
	}
	if w := callOperatorScope(t, s, http.MethodGet, "/api/operator-scope", nil); w.Code != http.StatusOK {
		t.Fatalf("operator get = %d %s", w.Code, w.Body.String())
	}
}

// The POST validates the file contract and reports machine-readable codes.
func TestOperatorScopeCreateValidates(t *testing.T) {
	s, _, dir := newOperatorScopeServer(t, &blockingExec{})
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"bad kind", map[string]any{"kind": "delete", "summary": "s"}},
		{"empty summary", map[string]any{"kind": "stop", "summary": "   "}},
		{"long summary", map[string]any{"kind": "stop", "summary": strings.Repeat("字", store.MaxOperatorScopeSummaryRunes+1)}},
		{"bad id", map[string]any{"kind": "stop", "summary": "s", "id": "os/../x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := callOperatorScope(t, s, http.MethodPost, "/api/operator-scope", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d %s, want 400", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "operator_scope_invalid") {
				t.Fatalf("missing code: %s", w.Body.String())
			}
		})
	}
	if _, err := os.Stat(store.OperatorScopePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("invalid posts created the file: %v", err)
	}
}

// A double-clicked composer with an idempotency key appends once and replays the
// first row on the retry: append-only means no second line for one decision.
func TestOperatorScopeCreateIdempotent(t *testing.T) {
	s, _, dir := newOperatorScopeServer(t, &blockingExec{})
	body := map[string]any{
		"kind": "authorization", "summary": "允许新增资产 A",
		"idempotency_key": "composer-1", "entity_refs": []string{"host:a"},
	}
	first := callOperatorScope(t, s, http.MethodPost, "/api/operator-scope", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first = %d %s", first.Code, first.Body.String())
	}
	var firstResult operatorScopeCreateResult
	decodeScopeResponse(t, first, &firstResult)
	if !firstResult.Created || firstResult.Entry.ID == "" {
		t.Fatalf("first result = %+v", firstResult)
	}
	second := callOperatorScope(t, s, http.MethodPost, "/api/operator-scope", body)
	if second.Code != http.StatusOK {
		t.Fatalf("replay = %d %s", second.Code, second.Body.String())
	}
	var secondResult operatorScopeCreateResult
	decodeScopeResponse(t, second, &secondResult)
	if secondResult.Created || secondResult.Entry.ID != firstResult.Entry.ID || !secondResult.Entry.At.Equal(firstResult.Entry.At) {
		t.Fatalf("replay = %+v, want the first row unchanged", secondResult)
	}
	raw, err := os.ReadFile(store.OperatorScopePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "\n"); n != 1 {
		t.Fatalf("file has %d lines, want 1: %s", n, raw)
	}
}

// A stop only affects the directions the operator explicitly listed. Listing an
// entity_ref does not cancel anything (that is the late-fact annotation rule),
// and no direction is inferred from titles or interest entities.
func TestOperatorScopeStopCancelsOnlyListedDirections(t *testing.T) {
	s, st, _ := newOperatorScopeServer(t, &blockingExec{})
	a, _ := st.CreateIntent(model.Intent{Title: "意图 A", InterestEntityRefs: []string{"host:a"}})
	b, _ := st.CreateIntent(model.Intent{Title: "意图 B", InterestEntityRefs: []string{"host:a"}})
	c, _ := st.CreateIntent(model.Intent{Title: "意图 C"})
	for _, id := range []string{a.ID, b.ID} {
		if _, err := s.StartRun(id); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
	}
	waitFor(t, 5*time.Second, func() bool {
		ia, _ := st.GetIntent(a.ID)
		ib, _ := st.GetIntent(b.ID)
		return ia.Status == model.StatusRunning && ib.Status == model.StatusRunning
	})
	aRunID := func() string { in, _ := st.GetIntent(a.ID); return in.RunID }()
	bRunID := func() string { in, _ := st.GetIntent(b.ID); return in.RunID }()

	w := callOperatorScope(t, s, http.MethodPost, "/api/operator-scope", map[string]any{
		"kind": "stop", "summary": "停测 A",
		"intent_ids":  []string{a.ID},
		"entity_refs": []string{"host:a"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("stop = %d %s", w.Code, w.Body.String())
	}
	var result operatorScopeCreateResult
	decodeScopeResponse(t, w, &result)
	if result.Applied == nil || result.Applied.StopID != result.Entry.ID {
		t.Fatalf("applied = %+v", result.Applied)
	}
	if len(result.Applied.Intents) != 1 || result.Applied.Intents[0].ID != a.ID || result.Applied.Intents[0].Result != "held_run_canceled" {
		t.Fatalf("applied intents = %+v", result.Applied.Intents)
	}

	// A is held and its run is cancelled; B (same interest entity) and C are
	// untouched.
	waitFor(t, 5*time.Second, func() bool {
		ia, _ := st.GetIntent(a.ID)
		ra, _ := st.GetRun(aRunID)
		return ia.Status == model.StatusFailed && ia.Hold && ra.Status == model.RunCanceled
	})
	ra, _ := st.GetRun(aRunID)
	if !strings.Contains(ra.ExitReason, result.Entry.ID) {
		t.Fatalf("run exit reason %q does not reference stop %q", ra.ExitReason, result.Entry.ID)
	}
	ib, _ := st.GetIntent(b.ID)
	if ib.Hold || ib.Status != model.StatusRunning {
		t.Fatalf("direction B was affected by an unlisted stop: %+v", ib)
	}
	rb, _ := st.GetRun(bRunID)
	if rb.Status != model.RunRunning {
		t.Fatalf("direction B run = %s, want running", rb.Status)
	}
	ic, _ := st.GetIntent(c.ID)
	if ic.Hold || ic.Status != model.StatusPending {
		t.Fatalf("direction C (%s) was affected by the stop: %+v", ic.ID, ic)
	}

	// The GET view derives the hold from the ledger and shows the stop entry.
	get := callOperatorScope(t, s, http.MethodGet, "/api/operator-scope", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get = %d %s", get.Code, get.Body.String())
	}
	var view operatorScopeReadView
	decodeScopeResponse(t, get, &view)
	if view.Counts.Stops != 1 || len(view.Stops) != 1 {
		t.Fatalf("view = %+v", view)
	}
	if len(view.Stops[0].StoppedIntents) != 1 {
		t.Fatalf("stopped intents = %+v", view.Stops[0].StoppedIntents)
	}
	stopIntent := view.Stops[0].StoppedIntents[0]
	if stopIntent.ID != a.ID || !stopIntent.Exist || !stopIntent.Hold || stopIntent.Status != model.StatusFailed {
		t.Fatalf("derived stop intent = %+v", stopIntent)
	}
	if !strings.Contains(stopIntent.CancelReason, result.Entry.ID) {
		t.Fatalf("derived cancel reason %q does not reference the stop", stopIntent.CancelReason)
	}
}

// No explicit selection means "record the stop decision, cancel nothing". A
// listed entity_ref alone never cancels a direction.
func TestOperatorScopeStopWithoutSelectionOnlyRecords(t *testing.T) {
	s, st, _ := newOperatorScopeServer(t, &blockingExec{})
	in, _ := st.CreateIntent(model.Intent{Title: "意图 A", InterestEntityRefs: []string{"host:a"}})
	w := callOperatorScope(t, s, http.MethodPost, "/api/operator-scope", map[string]any{
		"kind": "stop", "summary": "停测记录（未选意图）",
		"entity_refs": []string{"host:a"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("stop = %d %s", w.Code, w.Body.String())
	}
	var result operatorScopeCreateResult
	decodeScopeResponse(t, w, &result)
	if result.Applied == nil || len(result.Applied.Intents) != 0 {
		t.Fatalf("applied = %+v, want no cancellation", result.Applied)
	}
	got, _ := st.GetIntent(in.ID)
	if got.Hold || got.Status != model.StatusPending {
		t.Fatalf("unselected direction changed: %+v", got)
	}
}

// Repeating a stop is safe: a replay with the same key writes one row, and a
// second stop decision finds the direction already held without cancelling it
// again (one run per direction, no duplicate work).
func TestOperatorScopeStopRepeatedSafe(t *testing.T) {
	s, st, _ := newOperatorScopeServer(t, &blockingExec{})
	in, _ := st.CreateIntent(model.Intent{Title: "意图 A"})
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, _ := st.GetIntent(in.ID)
		return got.Status == model.StatusRunning
	})
	body := map[string]any{
		"kind": "stop", "summary": "停测 A", "intent_ids": []string{in.ID},
		"idempotency_key": "stop-1",
	}
	first := callOperatorScope(t, s, http.MethodPost, "/api/operator-scope", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first stop = %d %s", first.Code, first.Body.String())
	}
	replay := callOperatorScope(t, s, http.MethodPost, "/api/operator-scope", body)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	var replayResult operatorScopeCreateResult
	decodeScopeResponse(t, replay, &replayResult)
	if replayResult.Created {
		t.Fatal("replay created a second entry")
	}
	waitFor(t, 5*time.Second, func() bool {
		got, _ := st.GetIntent(in.ID)
		return got.Status == model.StatusFailed && got.Hold
	})
	// A second, distinct stop decision is also safe: already held is a no-op,
	// not a second cancellation.
	second := callOperatorScope(t, s, http.MethodPost, "/api/operator-scope", map[string]any{
		"kind": "stop", "summary": "再次停测 A", "intent_ids": []string{in.ID},
	})
	if second.Code != http.StatusCreated {
		t.Fatalf("second stop = %d %s", second.Code, second.Body.String())
	}
	var secondResult operatorScopeCreateResult
	decodeScopeResponse(t, second, &secondResult)
	if len(secondResult.Applied.Intents) != 1 || secondResult.Applied.Intents[0].Result != "already_held" {
		t.Fatalf("second applied = %+v", secondResult.Applied.Intents)
	}
	runs := 0
	for _, r := range st.Runs() {
		if r.IntentID == in.ID {
			runs++
		}
	}
	if runs != 1 {
		t.Fatalf("repeat stop produced %d runs, want 1", runs)
	}
}

// The console board carries operator_scope and late_after_stop at the JSON layer;
// a harness session gets the plain board (operator scope is operator-side data).
func TestOperatorScopeBoardProjection(t *testing.T) {
	s, st, _ := newOperatorScopeServer(t, &blockingExec{})
	w := callOperatorScope(t, s, http.MethodPost, "/api/operator-scope", map[string]any{
		"kind": "stop", "summary": "停测资产 A", "entity_refs": []string{"host:a"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("stop = %d %s", w.Code, w.Body.String())
	}
	var stopResult operatorScopeCreateResult
	decodeScopeResponse(t, w, &stopResult)
	// Recorded after the stop on the explicitly listed entity -> late annotation.
	// The recorded time is set explicitly so the identity+time rule is tested
	// deterministically rather than racing the wall clock.
	fact, err := st.AddFact(model.Fact{Kind: "service", Title: "10.0.0.9:443", EntityRef: "host:a", Confidence: "observed", CreatedAt: stopResult.Entry.At.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	// A fact on another entity is not annotated.
	if _, err := st.AddFact(model.Fact{Kind: "host", Title: "other", EntityRef: "host:b", Confidence: "observed"}); err != nil {
		t.Fatal(err)
	}

	operatorBoard := callOperatorScope(t, s, http.MethodGet, "/api/board", nil)
	if operatorBoard.Code != http.StatusOK {
		t.Fatalf("board = %d %s", operatorBoard.Code, operatorBoard.Body.String())
	}
	var boardBody struct {
		OperatorScope *operatorScopeReadView `json:"operator_scope"`
		LateAfterStop map[string]struct {
			StopID string `json:"stop_id"`
		} `json:"late_after_stop"`
	}
	decodeScopeResponse(t, operatorBoard, &boardBody)
	if boardBody.OperatorScope == nil || boardBody.OperatorScope.Counts.Stops != 1 {
		t.Fatalf("board operator_scope = %+v", boardBody.OperatorScope)
	}
	if len(boardBody.OperatorScope.Stops[0].LateFactIDs) != 1 || boardBody.OperatorScope.Stops[0].LateFactIDs[0] != fact.ID {
		t.Fatalf("late fact ids = %+v", boardBody.OperatorScope.Stops[0].LateFactIDs)
	}
	late, ok := boardBody.LateAfterStop[fact.ID]
	if !ok || late.StopID != stopResult.Entry.ID {
		t.Fatalf("board late_after_stop = %+v", boardBody.LateAfterStop)
	}

	// A harness session must not receive the operator-only fields at all.
	harnessBoard := callOperatorScope(t, s, http.MethodGet, "/api/board", nil, withAgentSurface)
	if harnessBoard.Code != http.StatusOK {
		t.Fatalf("harness board = %d %s", harnessBoard.Code, harnessBoard.Body.String())
	}
	raw := harnessBoard.Body.String()
	if strings.Contains(raw, "operator_scope") || strings.Contains(raw, "late_after_stop") {
		t.Fatalf("harness board leaked operator scope: %s", raw)
	}
}

// The graph projection carries late_after_stop for the facts in its page; the
// harness-session graph (agent mux) keeps the untouched shape.
func TestOperatorScopeGraphProjection(t *testing.T) {
	s, st, _ := newOperatorScopeServer(t, &blockingExec{})
	w := callOperatorScope(t, s, http.MethodPost, "/api/operator-scope", map[string]any{
		"kind": "stop", "summary": "停测资产 A", "entity_refs": []string{"host:a"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("stop = %d %s", w.Code, w.Body.String())
	}
	var stopResult operatorScopeCreateResult
	decodeScopeResponse(t, w, &stopResult)
	fact, err := st.AddFact(model.Fact{Kind: "service", Title: "10.0.0.9:443", EntityRef: "host:a", Confidence: "observed", CreatedAt: stopResult.Entry.At.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	w = callOperatorScope(t, s, http.MethodGet, "/api/graph", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("graph = %d %s", w.Code, w.Body.String())
	}
	var graphBody struct {
		Facts         []model.Fact `json:"facts"`
		LateAfterStop map[string]struct {
			StopID string `json:"stop_id"`
		} `json:"late_after_stop"`
	}
	decodeScopeResponse(t, w, &graphBody)
	late, ok := graphBody.LateAfterStop[fact.ID]
	if !ok || late.StopID != stopResult.Entry.ID {
		t.Fatalf("graph late_after_stop = %+v (facts=%d, want %s -> %s)", graphBody.LateAfterStop, len(graphBody.Facts), fact.ID, stopResult.Entry.ID)
	}
}
