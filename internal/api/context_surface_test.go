package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kksty/saker/internal/collab"
)

// A working-context delivery belongs to a harness session. A console request has
// no session marker and no participant identity, so serving it as the commander
// materialised a delivery nobody asked for; the heartbeat reads a due delivery as
// unacknowledged work and buys a round, so merely inspecting the context from the
// console could wake the commander and make it dispatch real work.
func TestConsoleContextReadDoesNotQueueACommanderDelivery(t *testing.T) {
	s, _, st := newCommanderServer(t)
	if _, ok := st.PendingDelivery("commander"); ok {
		t.Fatal("test setup: a commander delivery already exists")
	}
	rec := call(t, s, http.MethodGet, "/api/context", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("console GET /api/context = %d, want the absent session surface: %s", rec.Code, rec.Body.String())
	}
	if _, ok := st.PendingDelivery("commander"); ok {
		t.Fatal("a console context read queued a commander delivery")
	}
}

// ACK is the same session-only surface: a console ACK would clear the commander's
// pending delivery and advance its attention checkpoint, silently swallowing the
// events that page carried.
func TestConsoleContextAckCannotClearACommanderDelivery(t *testing.T) {
	s, _, st := newCommanderServer(t)
	mux := requestMux(s)
	if rec := record(mux, harnessRequest(http.MethodGet, "/api/context")); rec.Code != http.StatusOK {
		t.Fatalf("commander GET /api/context = %d: %s", rec.Code, rec.Body.String())
	}
	pending, ok := st.PendingDelivery("commander")
	if !ok {
		t.Fatal("commander read did not materialise its delivery")
	}
	rec := call(t, s, http.MethodPost, "/api/context/ack", map[string]any{"delivery_id": pending.ID})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("console POST /api/context/ack = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if _, ok := st.PendingDelivery("commander"); !ok {
		t.Fatal("a console ACK cleared the commander's pending delivery")
	}
}

// The commander session keeps its page: its identity is deliberately not mounted
// on unscoped project reads, but the gate marks the surface. A second read replays
// the same pending delivery instead of creating another one.
func TestCommanderContextReadKeepsItsDelivery(t *testing.T) {
	s, _, st := newCommanderServer(t)
	mux := requestMux(s)
	first := readDeliveryID(t, record(mux, harnessRequest(http.MethodGet, "/api/context")))
	if first == "" {
		t.Fatal("commander context read returned no delivery id")
	}
	pending, ok := st.PendingDelivery("commander")
	if !ok || pending.ID != first {
		t.Fatalf("pending commander delivery = %+v, want %s", pending, first)
	}
	second := readDeliveryID(t, record(mux, harnessRequest(http.MethodGet, "/api/context")))
	if second != first {
		t.Fatalf("second read created a new delivery %q, want the pending %q", second, first)
	}
}

// An executor reads its own direction's delivery, not the commander's.
func TestExecutorContextReadIsScopedToItsDirection(t *testing.T) {
	s, _, st := newCommanderServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/context", nil)
	req = req.WithContext(WithIdentity(req.Context(), collab.Identity{
		ParticipantID: "in_scoped", IntentID: "in_scoped", RunID: "r_scoped",
	}))
	if rec := record(requestMux(s), req); rec.Code != http.StatusOK {
		t.Fatalf("executor GET /api/context = %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := st.PendingDelivery("intent:in_scoped"); !ok {
		t.Fatal("executor read did not materialise its direction's delivery")
	}
	if _, ok := st.PendingDelivery("commander"); ok {
		t.Fatal("an executor read queued a commander delivery")
	}
}

func readDeliveryID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/context = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		DeliveryID string `json:"delivery_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode context: %v (%s)", err, rec.Body.String())
	}
	return out.DeliveryID
}
