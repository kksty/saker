package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// deleteProject and updateProject both end at the same manifest and the same
// kit. The delete holds the project's lifecycle lock for its whole run, so a
// settings save that arrived while it was working waits and then sees the
// project gone instead of writing project.json into a removed directory or
// re-arming the review pump of a canceled runtime.
//
// Before the lock existed there was nothing to wait on: this delete returned
// immediately while the simulated save still held the project.
func TestDeleteWaitsForInFlightSettingsUpdate(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{NoDecider: true})
	p, err := mgr.Create("busy", "acme.example")
	if err != nil {
		t.Fatal(err)
	}

	// Stand in for a settings save inside its critical section.
	release := p.LockLifecycle()
	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, "/api/projects/"+p.ID+"?confirm="+p.ID, nil)
		h.deleteProject(rec, r, p.ID)
		done <- rec.Code
	}()

	select {
	case code := <-done:
		release()
		t.Fatalf("delete finished with %d while a settings update held the project: the two operations are not serialised", code)
	case <-time.After(200 * time.Millisecond):
	}
	release()

	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("delete after the update released the project = %d, want 200", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete did not finish after the settings update released the project")
	}
	if mgr.Get(p.ID) != nil {
		t.Fatal("project survived the delete")
	}
	if _, err := os.Stat(p.Dir); !os.IsNotExist(err) {
		t.Fatalf("deleted project directory still exists: %v", err)
	}
}

// PUT /api/projects/{id} used to end with h.info(h.mgr.Get(id)) and no nil
// check: a delete that removed the project between the first Get and that last
// call sent nil into h.info, whose first line dereferences p.Store. The whole
// connection died instead of answering.
//
// The concurrent form below is the regression guard for the status codes. The
// panic window itself is scheduling-dependent, so the deterministic proof is
// the lifecycle-lock test above; here we only require that every interleaving
// ends in one of the honest answers and in a clean final state.
func TestConcurrentSettingsUpdateAndDeleteNeverPanic(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{NoDecider: true})
	const rounds = 20
	for i := 0; i < rounds; i++ {
		p, err := mgr.Create("race", "acme.example")
		if err != nil {
			t.Fatal(err)
		}
		// Attach a real runtime: the settings save then takes the live path
		// (reviews switch re-arm / settings push) that used to run against the
		// runtime the delete had already canceled.
		if k := h.attach(p); k == nil {
			t.Fatalf("round %d: create attached no runtime", i)
		}
		var (
			wg       sync.WaitGroup
			putCode  int
			delCode  int
			putBody  string
			delBody  string
			panicked = make(chan any, 2)
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicked <- r
				}
			}()
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPut, "/api/projects/"+p.ID, strings.NewReader(`{"reviews_disabled":false}`))
			h.updateProject(rec, r, p.ID)
			putCode, putBody = rec.Code, rec.Body.String()
		}()
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicked <- r
				}
			}()
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodDelete, "/api/projects/"+p.ID+"?confirm="+p.ID, nil)
			h.deleteProject(rec, r, p.ID)
			delCode, delBody = rec.Code, rec.Body.String()
		}()
		wg.Wait()
		select {
		case v := <-panicked:
			t.Fatalf("round %d: handler panicked: %v", i, v)
		default:
		}

		if delCode != http.StatusOK {
			t.Fatalf("round %d: delete = %d %s, want 200", i, delCode, delBody)
		}
		// A 200 means the save ran while the project still existed; a 404 means
		// the delete won the race; a 409 means the save found the project busy
		// (the delete already held its lifecycle lock) and said so instead of
		// queueing behind the drain. Anything else (notably the old nil-deref
		// crash or a 500 from a write into the removed tree) is a defect.
		if putCode != http.StatusOK && putCode != http.StatusNotFound && putCode != http.StatusConflict {
			t.Fatalf("round %d: update = %d %s, want 200, 404 or 409", i, putCode, putBody)
		}
		if mgr.Get(p.ID) != nil {
			t.Fatalf("round %d: project survived the delete", i)
		}
		if _, err := os.Stat(p.Dir); !os.IsNotExist(err) {
			t.Fatalf("round %d: deleted directory still exists: %v", i, err)
		}
		if h.kit(p.ID) != nil {
			t.Fatalf("round %d: a canceled runtime was left registered", i)
		}
	}
}

// A settings save that arrives while the delete is working must answer at once:
// the delete holds the lifecycle lock across its cancel + settle (a 10s budget
// for a run that ignores the cancel) and the tree removal, and the console's
// request has no timeout, so queuing would leave the dialog spinning with no
// answer. It reports project_busy instead.
func TestSettingsUpdateWhileDeleteRunsReportsBusy(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{NoDecider: true})
	p, err := mgr.Create("busy", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	// Stand in for a delete inside its critical section.
	release := p.LockLifecycle()
	defer release()
	type answer struct {
		code int
		body string
	}
	done := make(chan answer, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.updateProject(rec, httptest.NewRequest(http.MethodPut,
			"/api/projects/"+p.ID, strings.NewReader(`{"reviews_disabled":false}`)), p.ID)
		done <- answer{code: rec.Code, body: rec.Body.String()}
	}()

	select {
	case got := <-done:
		if got.code != http.StatusConflict {
			t.Fatalf("update during a delete = %d %s, want 409", got.code, got.body)
		}
		// Another 409 branch must not satisfy this guard: the console shows the
		// code's own sentence, and the operator's retry depends on knowing the
		// project is busy rather than gone.
		if !strings.Contains(got.body, `"project_busy"`) {
			t.Fatalf("update during a delete answered %s, want code project_busy", got.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("update blocked behind the delete's lifecycle lock instead of reporting busy")
	}
}

// A settings save on an id that is already gone is a plain 404 for the console
// to drop, not a panic and not a 500. This is the post-delete path (the first
// Manager.Get sees nothing); the guard for the old nil dereference inside a
// live request is TestConcurrentSettingsUpdateAndDeleteNeverPanic.
func TestSettingsUpdateAfterDeleteIs404(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{NoDecider: true})
	p, err := mgr.Create("gone", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.deleteProject(rec, httptest.NewRequest(http.MethodDelete,
		"/api/projects/"+p.ID+"?confirm="+p.ID, nil), p.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.updateProject(rec, httptest.NewRequest(http.MethodPut,
		"/api/projects/"+p.ID, strings.NewReader(`{"reviews_disabled":false}`)), p.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("update of a deleted project = %d %s, want 404", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(p.Dir); !os.IsNotExist(err) {
		t.Fatalf("deleted directory was recreated by the update: %v", err)
	}
}
