package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// newWorkerFixture is an agent fixture that can actually dispatch: a local node
// is bound and the executor returns immediately unless a test swaps in the
// blocking one.
func newWorkerFixture(t *testing.T) *agentFixture {
	t.Helper()
	f := newAgentFixture(t)
	if err := f.p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	f.srv.LocalNode = "local"
	f.srv.Exec = &recordExec{}
	return f
}

type historicalWorker struct {
	Intent model.Intent
	RunID  string
}

func startHistoricalWorker(t *testing.T, f *agentFixture) historicalWorker {
	t.Helper()
	intent, err := f.p.Store.CreateIntent(model.Intent{Title: "历史执行者", CreatedBy: f.srcID.ParticipantID})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := f.srv.StartRun(intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return historicalWorker{Intent: intent, RunID: runID}
}

func TestAgentWorkersListShowsRunStateWithoutStopPermission(t *testing.T) {
	f := newWorkerFixture(t)
	spawned := startHistoricalWorker(t, f)
	waitRun(t, f.p, spawned.RunID)
	// A direction created by someone else in the same project is visible but not
	// killable by this run.
	if _, err := f.p.Store.UpdateIntent(f.target.ID, func(i *model.Intent) {
		i.CreatedBy = "pt_in_someone_else"
	}); err != nil {
		t.Fatal(err)
	}
	// A still-active worker this run created is killable.
	owned, err := f.p.Store.CreateIntent(model.Intent{Title: "待跑的自己的 worker", CreatedBy: f.srcID.ParticipantID})
	if err != nil {
		t.Fatal(err)
	}

	rec := f.call(t, f.srcID, http.MethodGet, "/api/workers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("workers: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Workers []workerView `json:"workers"`
		Count   int          `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != len(resp.Workers) || resp.Count < 3 {
		t.Fatalf("count=%d workers=%d", resp.Count, len(resp.Workers))
	}
	byID := map[string]workerView{}
	for _, w := range resp.Workers {
		byID[w.IntentID] = w
	}
	mine, ok := byID[spawned.Intent.ID]
	if !ok || mine.CanKill || mine.Model == "" {
		// The spawned run already finished: can_kill is false because kill would
		// answer 409, not because the caller lost ownership.
		t.Fatalf("finished worker view = %+v ok=%v", mine, ok)
	}
	active, ok := byID[owned.ID]
	if !ok || active.CanKill {
		t.Fatalf("active owned worker view = %+v ok=%v", active, ok)
	}
	other, ok := byID[f.target.ID]
	if !ok || other.CanKill {
		t.Fatalf("foreign worker view = %+v ok=%v", other, ok)
	}
}
