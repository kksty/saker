package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
)

// The admission mode is a constant, so this route is read-only. A write would
// only restate the value the manifest already holds; serving one would look like
// an operator decision and change nothing. The gate: restoring a PUT handler
// turns this red.
func TestPremisePolicyIsReadOnly(t *testing.T) {
	s, p := newBindingServer(t)
	rec := call(t, s, http.MethodGet, "/api/premise-policy", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"premise_policy":"verified_only"`) {
		t.Fatalf("default policy = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"effective":"verified_only"`) {
		t.Fatalf("effective policy missing = %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []map[string]any{
		{"premise_policy": "verified_only"},
		{"premise_policy": "standard"},
		{"premise_policy": "strict"},
		{"premise_policy": "sometimes"},
	} {
		rec = call(t, s, http.MethodPut, "/api/premise-policy", body)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("write %v = %d %s, want 405 (no write route)", body, rec.Code, rec.Body.String())
		}
	}
	if got := s.premisePolicy(); got != project.PremisePolicyVerifiedOnly {
		t.Fatalf("effective policy after rejected writes = %q", got)
	}
	if p.PremisePolicy != project.PremisePolicyVerifiedOnly {
		t.Fatalf("stored policy after rejected writes = %q", p.PremisePolicy)
	}
}

func TestNoPremiseDirectionRunsUnheldWhateverItsEntityRefs(t *testing.T) {
	s, _ := newBindingServer(t)
	fact, err := s.Store.AddFact(model.Fact{
		Kind: "host", Title: "10.0.0.5", EntityRef: "host:10.0.0.5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.CreateIntent(model.Intent{Title: "blocked consumer", FromFactIDs: []string{fact.ID}}); err != nil {
		t.Fatal(err)
	}
	newIntent := func(refs string) (int, model.Intent) {
		req := harnessRequest(http.MethodPost, "/api/intents")
		req.Body = io.NopCloser(strings.NewReader(`{"title":"root direction","interest_entity_refs":` + refs + `}`))
		rec := serveMux(t, s, req)
		var created model.Intent
		_ = json.Unmarshal(rec.Body.Bytes(), &created)
		return rec.Code, created
	}
	for _, refs := range []string{`["host:10.0.0.5"]`, `["host:10.0.0.99"]`, `[]`} {
		if code, in := newIntent(refs); code != http.StatusCreated || in.Hold || in.Bootstrap {
			t.Fatalf("no-premise direction refs=%s = %d %+v, want created and unheld", refs, code, in)
		}
	}
}

func TestTopLevelCreationValidatesPremiseExistence(t *testing.T) {
	s, p := newBindingServer(t)
	rec := call(t, s, http.MethodPost, "/api/intents", map[string]any{
		"title": "bad premise", "from_fact_ids": []string{"f_missing"},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "fact_ref_missing") {
		t.Fatalf("missing premise = %d %s", rec.Code, rec.Body.String())
	}
	if len(p.Store.Snapshot().Intents) != 0 {
		t.Fatal("a rejected creation still wrote an intent")
	}
}
