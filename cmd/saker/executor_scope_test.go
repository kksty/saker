package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/access"
	"github.com/kksty/saker/internal/api"
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
	"github.com/kksty/saker/internal/testutil"
)

// The project mux and the participant mux mount the same battlefield handlers.
// An executor may read the shared situation on /p/<id>/, but the handlers that
// narrow a read by direction must narrow it on that base too: another
// direction's run output/context/journal and someone else's live browser
// binding are not battlefield situation. Before the gate attached the resolved
// identity, all of those failed open because identityOf(r) was false.
func TestExecutorCredentialIsScopedOnProjectMux(t *testing.T) {
	root := t.TempDir()
	mgr, err := project.Open(filepath.Join(root, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("scope", "")
	if err != nil {
		t.Fatal(err)
	}

	other, err := p.Store.CreateIntent(model.Intent{Title: "别的意图"})
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := p.Store.AddRun(model.Run{IntentID: other.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateRun(otherRun.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(p.Dir, "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	// The line carries the schema version the writer stamps (journal.go's
	// versionedJournalLine): a fixture without `v` is a pre-version journal, and
	// the current reader refuses it as unsupported rather than guessing.
	journal := fmt.Sprintf(`{"v":%d,"kind":"tool_result","call_id":"call_1","raw":{"tool_result":{"output":[{"text":{"text":"OTHER-DIRECTION-OUTPUT"}}]}}}`, executor.JournalSchemaVersion) + "\n"
	if err := os.WriteFile(filepath.Join(runs, otherRun.ID+".jsonl"), []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}

	browser, err := access.Create(p.CaseDir, access.Input{
		Name: "共享浏览器", Kind: "browser", CDPURL: "http://127.0.0.1:9222",
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := api.New(p.Store, nil, p.Dir, p.CaseDir)
	mux := http.NewServeMux()
	srv.Register(mux)
	h := &hub{mgr: mgr, kits: map[string]*kit{p.ID: {proj: p, srv: srv, mux: mux}}}

	registry := collab.NewRegistry(time.Hour)
	executor, err := registry.Issue(collab.Identity{
		ProjectID: p.ID, ParticipantID: collab.ParticipantID("in_me"), IntentID: "in_me",
		RunID: "r_me", Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	commander, err := registry.Issue(collab.Identity{ProjectID: p.ID, ParticipantID: collab.CommanderID, RunID: "r_cmd"})
	if err != nil {
		t.Fatal(err)
	}
	handler := newConsoleAuth("console-token", registry).wrap(h)

	get := func(token, path string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	prefix := "/p/" + p.ID + "/api"

	// A direction's execution record is private to that direction.
	for _, path := range []string{
		prefix + "/runs/" + otherRun.ID + "/output?call_id=call_1",
		prefix + "/runs/" + otherRun.ID + "/context",
		prefix + "/runs/" + otherRun.ID + "/events",
	} {
		if code, body := get(executor.Token, path); code != http.StatusForbidden {
			t.Errorf("executor GET %s = %d %q, want 403", path, code, body)
		}
	}
	// A browser binding carries a live CDP session: it is lease-scoped, and a
	// participant without the lease must not be able to read it.
	if code, body := get(executor.Token, prefix+"/access/"+browser.ID+"/material"); code != http.StatusConflict {
		t.Errorf("executor browser material = %d %q, want 409", code, body)
	}
	code, body := get(executor.Token, prefix+"/access")
	if code != http.StatusOK {
		t.Fatalf("executor access list = %d %q", code, body)
	}
	if strings.Contains(body, browser.Session) {
		t.Errorf("executor access list leaked another direction's browser session: %s", body)
	}

	// The shared battlefield stays readable: the point of the fact-intent
	// graph is that directions see the same situation.
	if code, body := get(executor.Token, prefix+"/facts"); code != http.StatusOK {
		t.Errorf("executor facts = %d %q, want 200", code, body)
	}

	// The commander coordinates the whole project and keeps the project-wide
	// view: it has no single direction to be scoped to.
	if code, body := get(commander.Token, prefix+"/runs/"+otherRun.ID+"/output?call_id=call_1"); code != http.StatusOK || !strings.Contains(body, "OTHER-DIRECTION-OUTPUT") {
		t.Errorf("commander run output = %d %q, want 200 with the output", code, body)
	}
	if code, body := get(commander.Token, prefix+"/runs/"+otherRun.ID+"/context"); code != http.StatusOK {
		t.Errorf("commander run context = %d %q, want 200", code, body)
	}
	if code, body := get(commander.Token, prefix+"/runs/"+otherRun.ID+"/events"); code != http.StatusOK {
		t.Errorf("commander run events = %d %q, want 200", code, body)
	}
	if code, body := get(commander.Token, prefix+"/access/"+browser.ID+"/material"); code != http.StatusOK || !strings.Contains(body, browser.Session) {
		t.Errorf("commander browser material = %d %q, want 200 with the binding", code, body)
	}
	if code, body := get(commander.Token, prefix+"/access"); code != http.StatusOK || !strings.Contains(body, browser.Session) {
		t.Errorf("commander access list = %d %q, want the session", code, body)
	}
}

// The bearer bucket and the login-form bucket are separate: a harness session
// retrying a stale token (the 401 text tells it to retry) must not lock the
// operator out of /login while both are 127.0.0.1. A stale cookie must not
// either — every console request carries it, and it is not a guess.
func TestSessionCredentialFailuresDoNotLockTheLoginForm(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := testutil.NewServer(t, withConsoleAuth(ok, "correct-horse-battery-staple", nil))
	defer srv.Close()

	client := &http.Client{}
	for i := 0; i < loginMaxFailures+2; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/board", nil)
		req.Header.Set("Authorization", "Bearer stale-token")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		want := http.StatusUnauthorized
		if i >= loginMaxFailures {
			want = http.StatusTooManyRequests
		}
		if res.StatusCode != want {
			t.Fatalf("bearer attempt %d = %d, want %d", i+1, res.StatusCode, want)
		}
	}

	// A stale cookie from a rotated console token: a normal page load must not
	// consume the form's budget either.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i := 0; i < loginMaxFailures+2; i++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/board", nil)
		req.AddCookie(&http.Cookie{Name: consoleCookie, Value: "1.deadbeef"})
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("stale cookie attempt %d = %d, want 401", i+1, res.StatusCode)
		}
	}

	res, err := noRedirect.PostForm(srv.URL+"/login", map[string][]string{"token": {"correct-horse-battery-staple"}})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("correct login after session-credential failures = %d, want 302", res.StatusCode)
	}
}
