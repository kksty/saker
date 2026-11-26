package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

func consoleCall(t *testing.T, s *Server, method, path, actor string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if actor != "" {
		req.Header.Set("X-Tw-Actor", actor)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The console roster sees the whole project and may kill any still-active
// worker; a finished worker is not killable (kill answers 409).
func TestConsoleWorkersAndKill(t *testing.T) {
	f := newWorkerFixture(t)
	blocking := &blockingExec{}
	f.srv.Exec = blocking
	// A worker created by an executor (so ownership would not allow the operator
	// if the operator were subject to the participant rule).
	owned, err := f.p.Store.CreateIntent(model.Intent{
		Title: "执行者创建的 worker", CreatedBy: "pt_in_someone_else",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.StartRun(owned.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := f.p.Store.GetIntent(owned.ID)
		if !ok || got.Status != model.StatusRunning {
			return false
		}
		return f.srv.Lifecycle.RunActive(got.RunID)
	})
	t.Cleanup(func() {
		f.srv.CancelAll()
		f.srv.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond)
	})
	finished, err := f.p.Store.CreateIntent(model.Intent{Title: "已结束的 worker"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(finished.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}

	rec := consoleCall(t, f.srv, http.MethodGet, "/api/workers", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("workers: %d %s", rec.Code, rec.Body.String())
	}
	var roster struct {
		Workers []workerView `json:"workers"`
		Count   int          `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &roster); err != nil {
		t.Fatal(err)
	}
	if roster.Count != len(roster.Workers) || roster.Count < 4 {
		t.Fatalf("count=%d workers=%d", roster.Count, len(roster.Workers))
	}
	byID := map[string]workerView{}
	for _, w := range roster.Workers {
		byID[w.IntentID] = w
	}
	if !byID[owned.ID].CanKill {
		t.Fatalf("active worker should be killable by operator: %+v", byID[owned.ID])
	}
	if byID[finished.ID].CanKill {
		t.Fatalf("finished worker should not be killable: %+v", byID[finished.ID])
	}

	rec = consoleCall(t, f.srv, http.MethodPost, "/api/workers/"+owned.ID+"/kill", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cancel_cause":"worker_kill"`) {
		t.Fatalf("kill: %d %s", rec.Code, rec.Body.String())
	}
	waitRun(t, f.p, byID[owned.ID].RunID)
	run, _ := f.p.Store.GetRun(byID[owned.ID].RunID)
	if run.Status != model.RunCanceled || run.CancelCause != model.CancelCauseWorkerKill {
		t.Fatalf("run = %s/%s", run.Status, run.CancelCause)
	}
	// finish writes the run's terminal state first and settles the intent right
	// after, so "the run is no longer running" is not yet "the intent is
	// settled": wait for the intent, not for the run.
	waitFor(t, 5*time.Second, func() bool {
		got, ok := f.p.Store.GetIntent(owned.ID)
		return ok && got.Status == model.StatusFailed && got.Hold
	}, func() string {
		got, _ := f.p.Store.GetIntent(owned.ID)
		run, _ := f.p.Store.GetRun(byID[owned.ID].RunID)
		return fmt.Sprintf("killed worker = %s hold=%v (run %s/%s)", got.Status, got.Hold, run.Status, run.CancelCause)
	})

	// A finished worker is a conflict, not a silent kill.
	rec = consoleCall(t, f.srv, http.MethodPost, "/api/workers/"+finished.ID+"/kill", "", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("terminal kill: %d %s", rec.Code, rec.Body.String())
	}
	if rec := consoleCall(t, f.srv, http.MethodPost, "/api/workers/in_missing/kill", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing worker: %d %s", rec.Code, rec.Body.String())
	}
}

// The operator may hang a spawned worker under any still-active direction; an
// executor-created parent is fine (unlike the participant route).
func TestWorkerViewCarriesTheParkReason(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.CreateIntent(model.Intent{Title: "EyouCMS WAF 编码层绕过"})
	if err != nil {
		t.Fatal(err)
	}
	parkedAt := time.Now()
	in, err = st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusParked
		i.Hold = true
		i.ParkReason = "测定完成：全阴性，不再重开"
		i.ParkedAt = &parkedAt
	})
	if err != nil {
		t.Fatal(err)
	}
	v := (&Server{Store: st}).workerViewOf(in, model.Run{}, workerCaller{operator: true})
	if v.ParkReason != "测定完成：全阴性，不再重开" || v.ParkedAt == nil || !v.ParkedAt.Equal(parkedAt) {
		t.Fatalf("worker view lost the park decision: %+v", v)
	}
}
