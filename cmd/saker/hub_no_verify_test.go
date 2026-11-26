package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kksty/saker/internal/node"
	"github.com/kksty/saker/internal/project"
)

// --no-verify is the boot state of the runtime review switch, not a "no verifier
// was built" fact: the operator can open reviews from the project page without a
// restart, so the verifier must exist and the switch must start closed.
func TestNoVerifyBootsWithTheRuntimeReviewSwitchClosed(t *testing.T) {
	dir := t.TempDir()
	mgr, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &hub{cfg: hubConfig{Nodes: &node.Registry{}, NoVerify: true}, mgr: mgr, ctx: ctx, kits: map[string]*kit{}}
	h.start()

	k := h.kits[p.ID]
	if k == nil {
		t.Fatal("boot did not attach the project")
	}
	if k.srv.Verifier == nil {
		t.Fatal("--no-verify skipped building the verifier: reviews could never be opened at runtime")
	}
	if !k.srv.ReviewsDisabled() || !k.srv.ReviewsPaused() {
		t.Fatalf("boot state = disabled %v paused %v, want both closed", k.srv.ReviewsDisabled(), k.srv.ReviewsPaused())
	}
	// The boot value is kept apart from the switch so an export can record both:
	// "started with --no-verify" survives the operator opening reviews later.
	if !k.srv.ReviewBootDisabled() {
		t.Fatal("boot value lost: export would claim reviews were never disabled at boot")
	}

	status := func() map[string]any {
		rec := httptest.NewRecorder()
		k.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/verifier", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/verifier = %d %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := status(); got["enabled"] != false || got["disabled"] != true {
		t.Fatalf("verifier status at boot = %v, want enabled=false disabled=true", got)
	}

	// Opening reviews is the operator's decision and it clears the boot closure.
	rec := httptest.NewRecorder()
	k.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/verifier/enable", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("enable = %d %s", rec.Code, rec.Body.String())
	}
	if k.srv.ReviewsDisabled() || k.srv.ReviewsPaused() {
		t.Fatalf("after enable = disabled %v paused %v, want both open", k.srv.ReviewsDisabled(), k.srv.ReviewsPaused())
	}
	if got := status(); got["enabled"] != true || got["disabled"] != false {
		t.Fatalf("verifier status after enable = %v, want enabled=true disabled=false", got)
	}

	// Closing it again and then asking resume to reopen it is refused: resume is
	// the temporary pause half, enable is the way back in.
	rec = httptest.NewRecorder()
	k.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/verifier/disable", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	k.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/verifier/resume", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("resume while disabled = %d %s, want 409", rec.Code, rec.Body.String())
	}
	var refusal map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal["code"] != "verify_disabled" {
		t.Fatalf("resume refusal = %v, want code verify_disabled", refusal)
	}
}
