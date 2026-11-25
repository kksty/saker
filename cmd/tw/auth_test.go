package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/api"
	"github.com/kksty/tianwu/internal/collab"
)

// Where a write came from must not depend on the model remembering a header.
// The agent token is the credential of a harness session on the battle surface,
// and the only harness session that talks to the project mux directly is the
// commander (executors arrive through /agent/). The gate therefore stamps the
// actor from the credential: a say without X-Tw-Actor used to land as
// `operator` in the ledger.
func TestAgentTokenStampsProjectWritesAsCommander(t *testing.T) {
	const operatorToken = "operator-token-for-the-auth-test"
	registry := collab.NewRegistry(time.Hour)
	commander := sessionCredential(t, registry, "prj-1")
	auth := newConsoleAuth(operatorToken, registry)
	if auth == nil {
		t.Fatal("no console auth was created")
	}
	var seen string
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Tw-Actor")
		w.WriteHeader(http.StatusOK)
	})
	wrapped := auth.wrap(protected)

	call := func(path, token string) int {
		t.Helper()
		seen = ""
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
		return rec.Code
	}

	// The commander's per-session credential on the project mux is attributed
	// structurally.
	if code := call("/p/prj-1/api/intents/in_1/say", commander.Token); code != http.StatusOK {
		t.Fatalf("session token on the project mux: %d", code)
	}
	if seen != collab.CommanderID {
		t.Fatalf("X-Tw-Actor = %q, want %q", seen, collab.CommanderID)
	}

	// The operator's own credential stays the operator: it authors messages
	// itself, so the gate must not relabel it.
	if code := call("/p/prj-1/api/intents/in_1/say", operatorToken); code != http.StatusOK {
		t.Fatalf("operator token on the project mux: %d", code)
	}
	if seen != "" {
		t.Fatalf("operator request was stamped as %q", seen)
	}

	// /agent/ is the participant surface: executors authenticate there with
	// their own per-run token, and their identity must not be replaced by the
	// commander's.
	if code := call("/agent/run/r_1/api/messages", commander.Token); code != http.StatusOK {
		t.Fatalf("/agent/ passthrough: %d", code)
	}
	if seen != "" {
		t.Fatalf("/agent/ request was stamped as %q", seen)
	}
}

// The manual "met" seal stamps a goal by hand, without the ledger's citation
// rule, so it belongs to the operator. The harness credential may judge
// completion with /complete (and reopen) and may create a goal only when the
// API can verify an explicit operator instruction (operator_message_id). The
// gate admits that definition path for the commander and mounts its identity;
// the handler re-checks the provenance, while hand-pinning stays operator-only.
// The request is also marked as a harness surface so the API can keep
// operator-side report/verification work out of its view.
func TestAgentTokenCannotHandPinAGoalButCanCompleteAndCreateWithProvenance(t *testing.T) {
	const token = "goal-ownership-operator-token"
	registry := collab.NewRegistry(time.Hour)
	commander := sessionCredential(t, registry, "prj-1")
	auth := newConsoleAuth(token, registry)
	if auth == nil {
		t.Fatal("no console auth was created")
	}
	var marked bool
	protected := auth.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marked = api.IsAgentSurface(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	call := func(method, path, credential string) (int, bool) {
		t.Helper()
		marked = false
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+credential)
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, req)
		return rec.Code, marked
	}

	agent := commander.Token
	operator := token
	for _, test := range []struct {
		method, path string
		credential   string
		want         int
	}{
		{http.MethodGet, "/p/prj-1/api/goals", agent, http.StatusOK},
		{http.MethodGet, "/p/prj-1/api/goals", operator, http.StatusOK},
		{http.MethodPost, "/p/prj-1/api/goals", agent, http.StatusOK},
		{http.MethodPost, "/p/prj-1/api/goals", operator, http.StatusOK},
		// The operator's hand seal is operator-only; the session's completion
		// (complete/reopen) stays available because judging the endpoint is its
		// job.
		{http.MethodPost, "/p/prj-1/api/goals/g_1/met", agent, http.StatusNotFound},
		{http.MethodPost, "/p/prj-1/api/goals/g_1/met", operator, http.StatusOK},
		{http.MethodPost, "/p/prj-1/api/goals/g_1/complete", agent, http.StatusOK},
		{http.MethodPost, "/p/prj-1/api/goals/g_1/reopen", agent, http.StatusOK},
	} {
		code, surface := call(test.method, test.path, test.credential)
		if code != test.want {
			t.Errorf("%s %s credential=%t = %d, want %d", test.method, test.path, test.credential == agent, code, test.want)
		}
		wantSurface := test.credential == agent && test.want != http.StatusNotFound
		if surface != wantSurface {
			t.Errorf("%s %s agent mark = %t, want %t", test.method, test.path, surface, wantSurface)
		}
	}
}

// next is attacker-controlled (a link to /login?next=…) and lands in the hidden
// form field. Escaping is what stops a reflected script on the console origin.
func TestLoginPageEscapesNext(t *testing.T) {
	page := renderLogin(`/"><script>alert(1)</script>`, "")
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Fatalf("next was written into the page unescaped")
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Fatalf("expected the escaped form in the field:\n%s", page)
	}
}

func TestSafeNextOnlyAllowsLocalPaths(t *testing.T) {
	cases := map[string]string{
		"/p/prj-1/":        "/p/prj-1/",
		"/":                "/",
		"":                 "/",
		"//evil.example":   "/",
		"https://evil/x":   "/",
		"javascript:alert": "/",
		`\\evil\share`:     "/",
		"/ok\r\nX-Bad: 1":  "/",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanKnowledgeCurationIsNotAvailableThroughAgentCredentials(t *testing.T) {
	const token = "knowledge-operator-test-token"
	registry := collab.NewRegistry(time.Hour)
	commander := sessionCredential(t, registry, "project")
	protected := newConsoleAuth(token, registry).wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for _, test := range []struct{ method, path string }{
		{"DELETE", "/p/project/api/playbooks/pb_1"},
		{"POST", "/p/project/api/playbooks/pb_1/kb-candidate"},
		{"DELETE", "/api/kb/entries/kb001"},
		{"POST", "/api/kb/candidates/id/publish"},
	} {
		for _, agent := range []bool{false, true} {
			req := httptest.NewRequest(test.method, test.path, nil)
			credential := token
			if agent {
				credential = commander.Token
			}
			req.Header.Set("Authorization", "Bearer "+credential)
			rec := httptest.NewRecorder()
			protected.ServeHTTP(rec, req)
			want := 200
			if agent {
				want = 404
			}
			if rec.Code != want {
				t.Fatalf("%s %s agent=%v: %d", test.method, test.path, agent, rec.Code)
			}
		}
	}
}

// An executor's credential reaches the project mux for the battlefield read
// side only: it can see the board, directions, goals, evidence, playbooks and
// runs (the shared situation the fact-intent graph is built on), but every
// write, the operator inbox and another project stay absent. Actions go through
// /agent/, where the handler can scope them to the caller's own intent.
func TestExecutorCredentialReadsBattlefieldOnly(t *testing.T) {
	const token = "operator-token-for-executor-read"
	registry := collab.NewRegistry(time.Hour)
	executor, err := registry.Issue(collab.Identity{
		ProjectID: "prj-1", ParticipantID: collab.ParticipantID("in_1"),
		IntentID: "in_1", RunID: "r_exec",
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := newConsoleAuth(token, registry)
	var actor string
	protected := auth.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor = r.Header.Get("X-Tw-Actor")
		w.WriteHeader(http.StatusOK)
	}))
	call := func(method, path string) int {
		t.Helper()
		actor = ""
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+executor.Token)
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, req)
		return rec.Code
	}

	for _, path := range []string{
		"/p/prj-1/api/board",
		"/p/prj-1/api/intents",
		"/p/prj-1/api/facts",
		"/p/prj-1/api/goals",
		"/p/prj-1/api/evidence",
		"/p/prj-1/api/playbooks",
		"/p/prj-1/api/runs",
		"/p/prj-1/api/graph",
		"/p/prj-1/api/context",
	} {
		if code := call(http.MethodGet, path); code != http.StatusOK {
			t.Errorf("GET %s = %d, want the battlefield read", path, code)
		}
		if actor != "" {
			t.Errorf("GET %s stamped %q; an executor is not the commander", path, actor)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/p/prj-1/api/intents/in_1/run"},
		{http.MethodPost, "/p/prj-1/api/intents/in_1/continue"},
		{http.MethodPost, "/p/prj-1/api/goals/g_1/complete"},
		{http.MethodPost, "/p/prj-1/api/project/standby"},
		{http.MethodPost, "/p/prj-1/api/messages"},
		{http.MethodPut, "/p/prj-1/api/state"},
		{http.MethodGet, "/p/prj-1/api/state"},
		{http.MethodGet, "/p/prj-1/api/operator-requests"},
		{http.MethodGet, "/p/prj-2/api/board"},
	} {
		if code := call(tc.method, tc.path); code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, code)
		}
	}
}

func TestHubCredentialSurvivesRestartWithoutSharingOperatorToken(t *testing.T) {
	const operator = "test-operator-key-abcdef123456"
	a := hubAccessToken(operator, "test-data")
	if len(a) != 64 || a == operator || a != hubAccessToken(operator, "test-data") {
		t.Fatal("Hub credential is not stable and separate")
	}
	if a == hubAccessToken(operator, "other-data") || a == hubAccessToken("changed-key", "test-data") {
		t.Fatal("Hub credential ignored instance identity")
	}
}

// TestCommanderSessionHasTheWholeIntentLifecycle: the commander decides whether
// a direction still deserves a run, so every intent state change is on its own
// credential — including reopen, which was the one action still missing from the
// list while the refusal told callers to use it.
func TestCommanderSessionHasTheWholeIntentLifecycle(t *testing.T) {
	const operatorCred = "lifecycle-operator-token"
	registry := collab.NewRegistry(time.Hour)
	commander := sessionCredential(t, registry, "prj-1")
	auth := newConsoleAuth(operatorCred, registry)
	if auth == nil {
		t.Fatal("no console auth was created")
	}
	protected := auth.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, action := range []string{"run", "park", "cancel", "retry", "reopen", "continue", "settle", "steer", "say"} {
		req := httptest.NewRequest(http.MethodPost, "/p/prj-1/api/intents/in_x/"+action, nil)
		req.Header.Set("Authorization", "Bearer "+commander.Token)
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("commander POST intents/in_x/%s = %d", action, rec.Code)
		}
	}
}
