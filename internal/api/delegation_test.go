package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
)

func TestReplacementTurnUpdatesWaitBeforeDispatch(t *testing.T) {
	for _, ready := range []bool{false, true} {
		name := "preserve_unfinished_wait"
		if ready {
			name = "consume_completed_wait"
		}
		t.Run(name, func(t *testing.T) {
			f := newParentFixture(t)
			ex := &blockingExec{}
			f.srv.Exec = ex
			t.Cleanup(func() { f.srv.CancelAll(); f.srv.WaitIdle(5 * time.Second) })
			request, err := f.srv.Store.RegisterOperatorWait(f.ident.IntentID, f.run.ID, model.OperatorWait{Request: "first login"})
			if err != nil {
				t.Fatal(err)
			}
			if ready {
				if err := f.srv.Store.CompleteOperatorWait(f.ident.IntentID, request.ID, "done"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.srv.Store.SettleIntentForRun(f.ident.IntentID, f.run.ID, model.RunDone); err != nil {
				t.Fatal(err)
			}
			if _, err := f.srv.Store.UpdateRun(f.run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
				t.Fatal(err)
			}
			if _, err := f.srv.Store.UpdateIntent(f.ident.IntentID, func(in *model.Intent) { in.Status = model.StatusPending }); err != nil {
				t.Fatal(err)
			}
			runID, err := f.srv.startRunWith(f.ident.IntentID, "continue with handoff", "", false, true)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, 5*time.Second, func() bool { return len(ex.requests()) == 1 })
			in := f.parent(t)
			if ready {
				if in.Wait != nil {
					t.Fatal("completed wait is still attached to the replacement turn")
				}
				if _, err := f.srv.Store.RegisterOperatorWait(in.ID, runID, model.OperatorWait{Request: "second login"}); err != nil {
					t.Fatal(err)
				}
			} else if in.Wait == nil || in.Wait.Operator.ID != request.ID || in.Wait.RegisteredByRun != runID {
				t.Fatalf("unfinished wait was not rebound before dispatch: %+v", in.Wait)
			}
		})
	}
}

func TestWaitRejectsInvalidJSONWithoutRegistering(t *testing.T) {
	for _, body := range []string{`{"timeout_seconds":"oops"}`, `{"mode":"all",`, `{"children":42}`} {
		t.Run(body, func(t *testing.T) {
			f := newParentFixture(t)
			req := httptest.NewRequest("POST", "/api/intents/"+f.ident.IntentID+"/wait", strings.NewReader(body))
			req = req.WithContext(WithIdentity(req.Context(), f.ident))
			rec := httptest.NewRecorder()
			f.srv.AgentHandler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || f.parent(t).Wait != nil {
				t.Fatalf("invalid JSON changed wait: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRetiredDelegationPathsDoNotCreateWork(t *testing.T) {
	f := newParentFixture(t)
	parent := "/api/intents/" + f.ident.IntentID
	before := len(f.p.Store.ListIntents())
	for _, path := range []string{"/api/spawn", parent + "/children"} {
		body := map[string]any{"title": "旧入口"}
		for _, rec := range []*httptest.ResponseRecorder{
			f.call(t, &f.ident, http.MethodPost, path, body),
			consoleCall(t, f.srv, http.MethodPost, path, "", body),
		} {
			if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("retired route %s returned %d: %s", path, rec.Code, rec.Body.String())
			}
		}
	}
	rec := f.call(t, &f.ident, http.MethodPost, parent+"/wait", map[string]any{"children": []string{"removed"}})
	if rec.Code != http.StatusBadRequest || f.parent(t).Wait != nil || len(f.p.Store.ListIntents()) != before {
		t.Fatalf("removed wait changed work: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWaitClampsHugeTimeoutBeforeDurationConversion(t *testing.T) {
	f := newParentFixture(t)
	action, _, err := f.p.Store.BeginAction(f.ident.IntentID, f.run.ID, "timeout", "历史动作", "")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	rec := f.call(t, &f.ident, "POST", "/api/intents/"+f.ident.IntentID+"/wait", map[string]any{"actions": []string{action.ID}, "timeout_seconds": int(^uint(0) >> 1)})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("wait: %d %s", rec.Code, rec.Body.String())
	}
	w := f.parent(t).Wait
	if w == nil || w.Deadline == nil || w.Deadline.Before(before.Add(maxWaitTimeout)) || w.Deadline.After(time.Now().Add(maxWaitTimeout)) {
		t.Fatalf("timeout was not clamped to 7 days: %+v", w)
	}
}

type parentFixture struct {
	srv   *Server
	p     *project.Project
	run   model.Run
	ident collab.Identity
}

func newParentFixture(t *testing.T) *parentFixture {
	t.Helper()
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	s.LocalNode = "local"
	s.Collab = collab.NewRegistry(time.Hour)
	s.MaxRuns = 0
	s.Exec = nodeExec{}
	parent, err := p.Store.CreateIntent(model.Intent{Title: "父意图", Description: "父", Doc: "操作员授权范围"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.Store.AddRun(model.Run{IntentID: parent.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(parent.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
		i.Attempts = 1
	}); err != nil {
		t.Fatal(err)
	}
	// The fixture's run stands for a dispatched run, so it carries the archived
	// dispatch a handoff is allowed to replace (the contract refuses a
	// replacement whose source has no archive).
	if _, err := p.Store.UpdateRun(run.ID, func(r *model.Run) { r.BriefPath = ".saker/briefs/" + r.ID + ".md" }); err != nil {
		t.Fatal(err)
	}
	parent, _ = p.Store.GetIntent(parent.ID)
	ident, err := s.Collab.Issue(collab.Identity{
		ProjectID: p.ID, ParticipantID: collab.ParticipantID(parent.ID),
		IntentID: parent.ID, RunID: run.ID, Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &parentFixture{srv: s, p: p, run: run, ident: ident}
}

func (f *parentFixture) call(t *testing.T, id *collab.Identity, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if id != nil {
		req = req.WithContext(WithIdentity(req.Context(), *id))
	}
	rec := httptest.NewRecorder()
	f.srv.AgentHandler().ServeHTTP(rec, req)
	return rec
}

func (f *parentFixture) parent(t *testing.T) model.Intent {
	t.Helper()
	in, ok := f.p.Store.GetIntent(f.ident.IntentID)
	if !ok {
		t.Fatal("parent missing")
	}
	return in
}

// waitNoRunning lets fixture goroutines finish writing before TempDir cleanup.
func waitNoRunning(t *testing.T, p *project.Project) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		running := false
		for _, r := range p.Store.Runs() {
			if r.Status == model.RunRunning {
				running = true
				break
			}
		}
		if !running {
			time.Sleep(20 * time.Millisecond)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a run never settled")
}

// Capacity 1: the parent must yield (waiting has no run) so the child can run,
// and the child's completion must wake the parent. This is the deadlock case
// docs/98 §17 names explicitly.
// After a restart, a waiting parent whose children already settled must be
// woken from the ledger alone; the wait (and results) are durable.
func TestRecoverWakesSatisfiedWait(t *testing.T) {
	f := newParentFixture(t)
	action, _, err := f.p.Store.BeginAction(f.ident.IntentID, f.run.ID, "before-restart", "fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.RegisterWait(f.ident.IntentID, f.run.ID, model.Wait{Actions: []string{action.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.ResolveAction(action.ID, action.Version, model.ActionCompleted, "done", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) {
		i.Status = model.StatusWaiting
		i.RunID = f.run.ID
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		now := time.Now()
		r.Status = model.RunDone
		r.EndedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	f.srv.Recover()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		parent, _ := f.p.Store.GetIntent(f.ident.IntentID)
		runs := 0
		for _, r := range f.p.Store.Runs() {
			if r.IntentID == f.ident.IntentID {
				runs++
			}
		}
		if runs >= 2 && parent.Wait == nil && parent.Status != model.StatusWaiting {
			waitNoRunning(t, f.p)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("satisfied wait was not recovered")
}

// The doc's completion case: two children on two profiles, one slot, parent
// waits, both results land, parent resumes.
