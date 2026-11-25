package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

func TestHandoffStartsNewSessionOnSameGateway(t *testing.T) {
	f := newParentFixture(t)
	action, _, err := f.p.Store.BeginAction(f.ident.IntentID, f.run.ID, "handoff-test", "fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's run was never dispatched; make the direction waiting so the
	// handoff exercises the no-live-turn path and still keeps a from-run id.
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) {
		i.Status = model.StatusWaiting
		i.RunID = f.run.ID
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.RegisterWait(f.ident.IntentID, f.run.ID, model.Wait{
		Actions: []string{action.ID}, Mode: model.WaitAll,
	}); err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, nil, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/handoff", map[string]any{
		"note": "换到慢模型继续核对", "expected_version": 0,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("handoff: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Intent  model.Intent  `json:"intent"`
		Handoff model.Handoff `json:"handoff"`
		RunID   string        `json:"run_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Intent.Profile != "" || resp.Intent.HandoffVersion != 1 || resp.Intent.Handoff == nil {
		t.Fatalf("intent = %+v", resp.Intent)
	}
	if resp.Handoff.ToProfile != "" || resp.Handoff.FromRun != f.run.ID ||
		resp.Handoff.Note != "换到慢模型继续核对" || resp.Handoff.Wait == nil || resp.Handoff.Wait.Actions[0] != action.ID {
		t.Fatalf("handoff = %+v", resp.Handoff)
	}
	if resp.RunID == "" {
		t.Fatal("no replacement run")
	}
	run, ok := f.p.Store.GetRun(resp.RunID)
	if !ok || run.HandoffFrom != f.run.ID || run.HandoffVersion != 1 {
		t.Fatalf("replacement run = %+v", run)
	}
	cur, _ := f.p.Store.GetIntent(f.ident.IntentID)
	if cur.Wait == nil || cur.Wait.RegisteredByRun != resp.RunID {
		t.Fatalf("wait not rebound: %+v", cur.Wait)
	}
	if cur.Profile != "" {
		t.Fatalf("profile = %q", cur.Profile)
	}
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		now := time.Now()
		r.Status = model.RunDone
		r.EndedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	waitNoRunning(t, f.p)
}

func TestHandoffVersionConflictAndFinishedIntent(t *testing.T) {
	f := newParentFixture(t)
	if _, err := f.p.Store.RegisterOperatorWait(f.ident.IntentID, f.run.ID, model.OperatorWait{Request: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) { i.Status = model.StatusWaiting }); err != nil {
		t.Fatal(err)
	}
	if rec := f.call(t, nil, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/handoff", map[string]any{
		"expected_version": 0,
	}); rec.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	// The fixture run was never dispatched; settle its row so the helper waits
	// only for the replacement run.
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		now := time.Now()
		r.Status = model.RunDone
		r.EndedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	waitNoRunning(t, f.p)
	// The same stale version cannot overwrite; the new version can.
	rec := f.call(t, nil, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/handoff", map[string]any{
		"expected_version": 0,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.call(t, nil, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/handoff", map[string]any{
		"expected_version": 1,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second: %d %s", rec.Code, rec.Body.String())
	}
	waitNoRunning(t, f.p)

	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if rec := f.call(t, nil, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/handoff", map[string]any{
		"expected_version": 2,
	}); rec.Code != http.StatusConflict {
		t.Fatalf("done handoff: %d %s", rec.Code, rec.Body.String())
	}
}

// A stale expected_version must fail before any side effect: the live run keeps
// running and the intent keeps its holder. Regression for the destructive CAS
// where the old ordering cancelled the run first and only then found the conflict.
func TestStaleHandoffDoesNotCancelLiveRun(t *testing.T) {
	f := newParentFixture(t)
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) { i.HandoffVersion = 1 }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) { r.Status = model.RunRunning }); err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, nil, http.MethodPost, "/api/intents/"+f.ident.IntentID+"/handoff", map[string]any{
		"expected_version": 0,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale handoff: %d %s", rec.Code, rec.Body.String())
	}
	cur, _ := f.p.Store.GetIntent(f.ident.IntentID)
	if cur.Status != model.StatusRunning || cur.RunID != f.run.ID || cur.Hold || cur.HandoffVersion != 1 {
		t.Fatalf("stale handoff changed the intent: %+v", cur)
	}
	run, _ := f.p.Store.GetRun(f.run.ID)
	if run.Status != model.RunRunning {
		t.Fatalf("stale handoff stopped the live run: %+v", run)
	}
	// Let the fixture settle its own row before TempDir cleanup.
	now := time.Now()
	if _, err := f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) {
		r.Status = model.RunDone
		r.EndedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) { i.Status = model.StatusPending }); err != nil {
		t.Fatal(err)
	}
	waitNoRunning(t, f.p)
}

func TestFinishRecordsUsage(t *testing.T) {
	f := newParentFixture(t)
	run, err := f.p.Store.AddRun(model.Run{
		IntentID: f.ident.IntentID, Node: "local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Store.UpdateIntent(f.ident.IntentID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
	}); err != nil {
		t.Fatal(err)
	}
	f.srv.finish(run.ID, f.ident.IntentID, model.RunDone, "", executor.Result{
		ExitCode: 0, StopReason: "completed", UsageKnown: true,
		InputTokens: 1_000_000, OutputTokens: 100_000, CachedInputTokens: 400_000,
		UsageRequests: 2, Tokens: 1_100_000,
	}, 0)
	got, _ := f.p.Store.GetRun(run.ID)
	if !got.Usage.Known || got.Usage.InputTokens != 1_000_000 || got.Usage.OutputTokens != 100_000 ||
		got.Usage.CachedInputTokens != 400_000 || got.Usage.Requests != 2 {
		t.Fatalf("usage = %+v", got.Usage)
	}
}
