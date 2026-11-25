package api

import (
	"strings"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// A premise written by a direction that has not settled is not a premise yet:
// the input freeze refuses it (store/input.go:102-104) and readiness says the
// same. The early gate used to read only the verdict half of that rule, so /run
// answered 202, built the run row, and failed inside the run at the freeze.
func TestEarlyGateRefusesAPremiseWhoseProducerIsStillLive(t *testing.T) {
	s, p := newBindingServer(t)
	src, err := p.Store.CreateIntent(model.Intent{Title: "来源意图", Description: "还在跑"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(src.ID, func(i *model.Intent) { i.Status = model.StatusRunning }); err != nil {
		t.Fatal(err)
	}
	f, err := p.Store.AddFact(model.Fact{IntentID: src.ID, Kind: "vuln", Title: "来源产出", Status: "unverified"})
	if err != nil {
		t.Fatal(err)
	}
	down, err := p.Store.CreateIntent(model.Intent{
		Title: "下游", Description: "引用一条还没收口的来源", FromFactIDs: []string{f.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	blocked := s.blockedPremises(down)
	if len(blocked) != 1 || !strings.Contains(blocked[0], "还没收口") {
		t.Fatalf("blockedPremises = %v, want the unsettled source named", blocked)
	}
	before := len(p.Store.Runs())
	rec := call(t, s, "POST", "/api/intents/"+down.ID+"/run", nil)
	if rec.Code != 409 {
		t.Fatalf("/run on an unsettled source = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if after := len(p.Store.Runs()); after != before {
		t.Fatalf("a refused dispatch still created a run row (%d -> %d)", before, after)
	}

	// Control: once the source has settled the same direction dispatches, so the
	// refusal above is about the producer, not about a verdict-less observation
	// being unusable in a project that cannot produce verdicts right now.
	if _, err := p.Store.UpdateIntent(src.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	s.verifyDisabled.Store(true)
	runID, err := s.StartRun(down.ID)
	if err != nil {
		t.Fatalf("settled source still refused: %v", err)
	}
	waitRun(t, p, runID)
}
