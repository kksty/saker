package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/project"
)

// Repro for 只创建 running anyway. Creation attached the project live, so the
// watchdog's first sweep read the freshly written endpoint goal as an
// unacknowledged journal change and woke the commander (decision log:
// wake=ledger, attention=goal_changed) seconds after a click that promised not
// to run anything. The creation now persists the not_started pause before
// attach: the decider is parked, no run row appears, and the same journal reason
// stays pending for the first explicit start.
func TestCreateOnlyLeavesTheNewProjectStopped(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{})
	t.Cleanup(func() { shutdownKits(h) })

	code, out := postProject(t, h, `{"name":"Acme","scope":"acme.example",
		"goal":"拿到边界主机","start":false}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	p := mgr.Get(out["id"].(string))
	if p == nil {
		t.Fatalf("project %v is not in the manager", out["id"])
	}
	if man := readManifest(t, p); !man.Paused || man.PauseKind != project.PauseKindNotStarted {
		t.Fatalf("manifest paused=%v kind=%q; 只创建 must persist the not_started pause",
			man.Paused, man.PauseKind)
	}
	k := h.kits[p.ID]
	if k == nil || k.dec == nil {
		t.Fatal("create did not attach a runtime with a decider")
	}
	// A parked decider is what makes "nothing runs" true: a wake cannot start a
	// round while it is paused, so no watchdog sweep can run the engagement.
	if !k.dec.Paused() {
		t.Fatal("the decider of a 只创建 project is armed; a journal sweep can start a round")
	}
	if runs := p.Store.Runs(); len(runs) != 0 {
		t.Fatalf("只创建 produced %d run(s): %+v", len(runs), runs)
	}
	// The charter is not acknowledged away: the first start must still see the
	// endpoint as a change to plan against.
	if a := p.Store.CommanderAttention(); len(a.Reasons) == 0 {
		t.Fatal("the endpoint goal is no longer pending attention; the first round would start blind")
	}
	// The console classifies the pause from the code, not from prose, and offers
	// the one way out.
	body := boardBody(t, k)
	if !strings.Contains(body, `"project_pause_kind":"not_started"`) {
		t.Fatalf("board does not report the creation pause: %s", body)
	}
	if !strings.Contains(body, `"reason_code":"paused_not_started"`) {
		t.Fatalf("resume action does not name the creation pause: %s", body)
	}
	if strings.Contains(body, `"reason_code":"paused_unrecorded"`) ||
		strings.Contains(body, `"reason_code":"paused_manual"`) {
		t.Fatalf("the creation pause was attributed to a cause it does not have: %s", body)
	}

	// 继续项目 is the explicit start: it clears the pause and re-arms the decider.
	rec := httptest.NewRecorder()
	k.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/project/resume", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("resume = %d %s", rec.Code, rec.Body.String())
	}
	if p.IsPaused() || k.dec.Paused() {
		t.Fatalf("resume left manifest paused=%v decider paused=%v", p.IsPaused(), k.dec.Paused())
	}
}

// 创建并开始 keeps the historical creation shape: a live project whose first
// round is requested by the creation request itself (not by a browser flag), so
// closing the tab that received the 201 cannot leave a project that never ran.
func TestCreateAndStartLeavesTheProjectLive(t *testing.T) {
	h, mgr := createOptionsHub(t, hubConfig{})
	t.Cleanup(func() { shutdownKits(h) })

	code, out := postProject(t, h, `{"name":"Acme","scope":"acme.example",
		"goal":"拿到边界主机","start":true}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	p := mgr.Get(out["id"].(string))
	if man := readManifest(t, p); man.Paused || man.PauseKind != "" {
		t.Fatalf("manifest paused=%v kind=%q; 创建并开始 must not persist a pause",
			man.Paused, man.PauseKind)
	}
	k := h.kits[p.ID]
	if k == nil || k.dec == nil {
		t.Fatal("create did not attach a runtime with a decider")
	}
	if k.dec.Paused() {
		t.Fatal("创建并开始 attached a paused decider; the first round can never start")
	}
	// The round this request queued still has the endpoint to plan against.
	if a := p.Store.CommanderAttention(); len(a.Reasons) == 0 {
		t.Fatal("the endpoint goal was consumed before any round could read it")
	}
}
