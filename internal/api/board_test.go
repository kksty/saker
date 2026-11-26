package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
	"github.com/kksty/saker/internal/testutil"
)

// The overview's guard panel reads board.guard: the watchdog must publish its
// cadence and idle threshold there instead of letting the page hardcode them.
func TestBoardPublishesGuardCadence(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, t.TempDir(), t.TempDir())
	s.IdleTimeout = 30 * time.Minute
	mux := http.NewServeMux()
	s.Register(mux)
	srv := testutil.NewServer(t, mux)
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/board")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got struct {
		Guard struct {
			IntervalSeconds    int        `json:"interval_seconds"`
			IdleTimeoutSeconds int        `json:"idle_timeout_seconds"`
			StartedAt          *time.Time `json:"started_at"`
			CompletedAt        *time.Time `json:"completed_at"`
		} `json:"guard"`
		Decider struct {
			Enabled bool `json:"enabled"`
		} `json:"decider"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Guard.IntervalSeconds != int(watchInterval/time.Second) {
		t.Fatalf("interval = %d, want %d", got.Guard.IntervalSeconds, int(watchInterval/time.Second))
	}
	if got.Guard.IdleTimeoutSeconds != 30*60 {
		t.Fatalf("idle timeout = %d, want 1800", got.Guard.IdleTimeoutSeconds)
	}
	if got.Guard.StartedAt != nil || got.Guard.CompletedAt != nil {
		t.Fatalf("watch timestamps before Watch ran = %+v", got.Guard)
	}
	if got.Decider.Enabled {
		t.Fatal("decider without a model must report disabled")
	}
}

// The briefing strip compares board.digest with decider.last_digest to say
// whether the next heartbeat will wake the commander. That comparison is only
// honest if the API publishes the value the decider hashes — including a change
// no count can see, like rewriting a fact's doc (D79).
func TestBoardPublishesWakeDigest(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{Kind: "note", Title: "一条观察"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, t.TempDir(), t.TempDir())
	mux := http.NewServeMux()
	s.Register(mux)
	srv := testutil.NewServer(t, mux)
	defer srv.Close()

	get := func() (digest, last string) {
		res, err := http.Get(srv.URL + "/api/board")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var got struct {
			Digest  string `json:"digest"`
			Decider struct {
				LastDigest string `json:"last_digest"`
			} `json:"decider"`
		}
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got.Digest, got.Decider.LastDigest
	}

	before, _ := get()
	if before == "" {
		t.Fatal("the board published no digest, so the page cannot compare it")
	}
	if _, err := st.UpdateFact(f.ID, func(f *model.Fact) { f.Doc = "重放：curl -s http://10.0.0.5:8080/" }); err != nil {
		t.Fatal(err)
	}
	after, _ := get()
	if after == before {
		t.Fatalf("a doc edit did not reach the published digest: %s", before)
	}
}

// The previous completion must remain visible while a sweep is blocked.
func TestGuardWatchCompletionWaitsForSweep(t *testing.T) {
	s := &Server{}
	previous := &watchTimes{started: time.Now().Add(-2 * time.Minute), completed: time.Now().Add(-time.Minute)}
	s.watchPulse.Store(previous)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	go func() {
		defer close(done)
		s.watchOnce(func() {
			close(started)
			<-release
		})
	}()
	<-started
	pending := s.watchPulse.Load()
	if !pending.started.After(previous.started) || !pending.inProgress || pending.completed != previous.completed {
		t.Fatalf("pending sweep = %+v, previous = %+v", pending, previous)
	}
	t.Cleanup(func() {
		<-done
		finished := s.watchPulse.Load()
		if finished.inProgress || finished.started != pending.started || !finished.completed.After(previous.completed) || finished.completed.Before(finished.started) {
			t.Fatalf("completed sweep = %+v, pending = %+v", finished, pending)
		}
	})
}

// Failed and skipped starts must not be presented as successful redispatches.
func TestGuardRunCountersUseDurableSuccessors(t *testing.T) {
	runs := []model.Run{
		{ID: "old", Status: model.RunCanceled, CancelCause: model.CancelCauseWatchdog},
		{ID: "legacy", Status: model.RunCanceled, CancelCause: model.CancelCauseWatchdog},
		{ID: "held", Status: model.RunCanceled, CancelCause: model.CancelCauseWatchdog,
			RedispatchDisposition: &model.RedispatchDisposition{State: "skipped", Reason: "意图挂起"}},
		{ID: "failed", Status: model.RunCanceled, CancelCause: model.CancelCauseWatchdog,
			RedispatchDisposition: &model.RedispatchDisposition{State: "failed", Reason: "无宿主"}},
	}
	var guard guardInfo
	guard.observeRuns(runs)
	if guard.GuardCanceled != 4 || guard.AutoRedispatched != 0 {
		t.Fatalf("before linked start = %+v", guard)
	}
	guard = guardInfo{}
	guard.observeRuns(append(runs[:1], model.Run{ID: "successor", AutoRedispatchedFrom: "old"}))
	if guard.GuardCanceled != 1 || guard.AutoRedispatched != 1 || len(guard.Recent) != 1 ||
		guard.Recent[0].RunID != "old" || guard.Recent[0].SuccessorRunID != "successor" {
		t.Fatalf("after linked start = %+v", guard)
	}
}

// Independent observations must survive unrelated harness events and failures.
func TestGuardActivitySeparatesInFlightToolFromHostAndFacts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in, err := st.CreateIntent(model.Intent{Title: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	_, r, err := st.StartIntentRun(in.ID, "fixture", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, t.TempDir(), t.TempDir())
	signal := func() guardActivity {
		db := st.Snapshot()
		return s.guardActivity(db, board.BuildFromDB(db, 20).Running)[0]
	}
	before := signal()
	if before.HostOnline != nil || before.OpenTools != 0 || before.TurnEnded {
		t.Fatalf("invented activity before harness events: %+v", before)
	}
	st.NoteHarnessEvent(r.ID, time.Now(), "tool_call", "fixture", "", "outer")
	st.NoteHarnessEvent(r.ID, time.Now(), "tool_call", "fixture", "", "inner")
	st.NoteHarnessEvent(r.ID, time.Now(), "tool_result", "fixture", "", "inner")
	st.NoteHarnessEvent(r.ID, time.Now(), "message", "", "", "")
	if next := signal(); next.OpenTools != 1 || next.HostOnline != nil || next.Facts != 0 {
		t.Fatalf("unrelated event erased tool or implied progress: %+v", next)
	}
	st.NoteHarnessEvent(r.ID, time.Now(), "turn_ended", "", "", "")
	if next := signal(); next.OpenTools != 0 || !next.TurnEnded {
		t.Fatalf("turn receipt not recorded: %+v", next)
	}
}
