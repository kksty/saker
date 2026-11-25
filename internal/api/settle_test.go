package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

// newSettleServer builds a quiet project with one direction parked at a chosen
// status and one recorded fact to cite; nothing is dispatched.
func newSettleServer(t *testing.T, status string, hold bool, heldBy string) (*Server, model.Intent, model.Fact) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &blockingExec{}, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0
	in, err := st.CreateIntent(model.Intent{Title: "拿到立足点", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{
		IntentID: in.ID, Kind: "host", Title: "10.0.0.5 可执行命令",
		Summary: "ssh 回显", Status: "verified", Confidence: "observed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = status
		i.Hold = hold
		i.HeldBy = heldBy
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := st.GetIntent(in.ID)
	if !ok {
		t.Fatal("intent disappeared")
	}
	return s, got, f
}

func settleResponse(t *testing.T, rec *httptest.ResponseRecorder) struct {
	Settled        bool         `json:"settled"`
	DoneBy         string       `json:"done_by"`
	DoneUnverified bool         `json:"done_unverified"`
	Intent         model.Intent `json:"intent"`
} {
	t.Helper()
	var resp struct {
		Settled        bool         `json:"settled"`
		DoneBy         string       `json:"done_by"`
		DoneUnverified bool         `json:"done_unverified"`
		Intent         model.Intent `json:"intent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return resp
}

// A failed direction whose work is visible in a recorded fact can be declared
// done, and the row carries who declared it plus what it rests on.
func TestSettleDeclaresDoneFromFailed(t *testing.T) {
	s, in, fact := newSettleServer(t, model.StatusFailed, true, model.HoldActorCommander)
	// A fact is never born verified; a passing review is what leaves the
	// verdict the completion is checked against.
	if _, err := s.Store.ReviewFact(model.Review{FactID: fact.ID, Verdict: "pass", VerifierRun: "run-proof"}); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/settle", map[string]any{
		"note":     "回显已证明同权命令，后续方向可以接着走",
		"fact_ids": []string{fact.ID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("settle = %d %s", rec.Code, rec.Body.String())
	}
	resp := settleResponse(t, rec)
	if !resp.Settled || resp.Intent.Status != model.StatusDone {
		t.Fatalf("settle result = %+v", resp)
	}
	if resp.DoneBy != model.HoldActorOperator {
		t.Fatalf("done_by = %q, want operator (console call)", resp.DoneBy)
	}
	if !resp.Intent.DoneDeclared() || resp.Intent.DoneDeclaredBy != model.HoldActorOperator {
		t.Fatalf("declared provenance = %+v", resp.Intent)
	}
	if resp.Intent.DoneNote == "" || len(resp.Intent.DoneFactIDs) != 1 || resp.Intent.DoneFactIDs[0] != fact.ID {
		t.Fatalf("settle evidence = %+v", resp.Intent)
	}
	if resp.Intent.DoneAt == nil {
		t.Fatal("done_at not stamped")
	}
	if resp.DoneUnverified {
		t.Fatal("a verified cited fact must not be reported unverified")
	}
	// The ledger row is what later readers see, not just the response.
	stored, _ := s.Store.GetIntent(in.ID)
	if stored.Status != model.StatusDone || stored.DoneDeclaredBy != model.HoldActorOperator {
		t.Fatalf("stored intent = %+v", stored)
	}
}

// An operator stop is not the commander's to flip, the same rule retry follows.
func TestSettleRefusesOperatorHold(t *testing.T) {
	s, in, fact := newSettleServer(t, model.StatusFailed, true, model.HoldActorOperator)
	rec := call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/settle", map[string]any{
		"note": "我认为已经达成", "fact_ids": []string{fact.ID},
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "held_by_operator") {
		t.Fatalf("settle on operator hold = %d %s", rec.Code, rec.Body.String())
	}
	got, _ := s.Store.GetIntent(in.ID)
	if got.Status != model.StatusFailed {
		t.Fatalf("refused settle changed the row: %+v", got)
	}
}

// A live direction is the run's to close, not the commander's.
func TestSettleRefusesLiveDirection(t *testing.T) {
	s, in, fact := newSettleServer(t, model.StatusRunning, false, "")
	rec := call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/settle", map[string]any{
		"note": "抢跑", "fact_ids": []string{fact.ID},
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "settle_conflict") {
		t.Fatalf("settle on running = %d %s", rec.Code, rec.Body.String())
	}
}

// The judgement must say why and cite something; an empty declaration is the
// board-cleaning behaviour this action is meant to be distinguishable from.
func TestSettleRequiresNoteAndEvidence(t *testing.T) {
	s, in, fact := newSettleServer(t, model.StatusFailed, true, model.HoldActorCommander)
	for _, body := range []map[string]any{
		{"fact_ids": []string{fact.ID}},
		{"note": "完成", "fact_ids": []string{}},
	} {
		rec := call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/settle", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("settle %v = %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

// A withdrawn fact cannot support a completion, and a missing id is refused
// rather than silently dropped.
func TestSettleRefusesUnusableOrMissingEvidence(t *testing.T) {
	s, in, fact := newSettleServer(t, model.StatusFailed, true, model.HoldActorCommander)
	if _, err := s.Store.UpdateFact(fact.ID, func(f *model.Fact) { f.Status = "withdrawn" }); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/settle", map[string]any{
		"note": "用被撤回的事实收口", "fact_ids": []string{fact.ID},
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "evidence_unusable") {
		t.Fatalf("settle with withdrawn fact = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/settle", map[string]any{
		"note": "引用不存在的事实", "fact_ids": []string{"f_nope"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("settle with missing fact = %d %s", rec.Code, rec.Body.String())
	}
}

// The commander surface records itself, and with reviews on the completion is a
// claim until the cited facts have verdicts.
// A declared completion belongs to the turn it closed: once the direction is
// parked and reopened (the path back to the queue for a done row), the old
// badge must not ride along; the settle note stays in Notes as history.
func TestSettleFieldsClearedOnReopen(t *testing.T) {
	s, in, fact := newSettleServer(t, model.StatusFailed, true, model.HoldActorCommander)
	rec := call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/settle", map[string]any{
		"note": "按已记录的事实收口", "fact_ids": []string{fact.ID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("settle = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/park", map[string]any{"reason": "先放一放"})
	if rec.Code != http.StatusOK {
		t.Fatalf("park = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/reopen", nil)
	if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("reopen = %d %s", rec.Code, rec.Body.String())
	}
	got, _ := s.Store.GetIntent(in.ID)
	if got.Status != model.StatusPending {
		t.Fatalf("reopened intent = %s, want pending", got.Status)
	}
	if got.DoneDeclared() || got.DoneNote != "" || got.DoneFactIDs != nil || got.DoneAt != nil {
		t.Fatalf("reopen kept the declared completion: %+v", got)
	}
}

func TestCommanderSettleStampsActorAndUnverified(t *testing.T) {
	s, in, _ := newSettleServer(t, model.StatusFailed, true, model.HoldActorCommander)
	// An unverified fact: reviews are on in this fixture, so the completion is
	// reported as a claim rather than hidden.
	unverifiedFact, err := s.Store.AddFact(model.Fact{
		IntentID: in.ID, Kind: "vuln", Title: "疑似越权", Summary: "待核",
		Status: "unverified", Confidence: "observed",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := strings.NewReader(`{"note":"先按已记录的事实收口","fact_ids":["` + unverifiedFact.ID + `"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/intents/"+in.ID+"/settle", body)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(WithAgentSurface(req.Context()))
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	s.Register(mux)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commander settle = %d %s", rec.Code, rec.Body.String())
	}
	resp := settleResponse(t, rec)
	if resp.DoneBy != model.HoldActorCommander || resp.Intent.DoneDeclaredBy != model.HoldActorCommander {
		t.Fatalf("actor = %q / %q", resp.DoneBy, resp.Intent.DoneDeclaredBy)
	}
	if !resp.DoneUnverified {
		t.Fatal("unverified evidence must be reported as a pending claim")
	}
}

// Settling a direction that still owns unstarted children must not leave them
// queued for a parent that will never read their results: the same sweep park
// runs applies here.
func TestSettleFailsUnstartedChildren(t *testing.T) {
	s, in, fact := newSettleServer(t, model.StatusFailed, true, model.HoldActorCommander)
	child, err := s.Store.CreateIntent(model.Intent{
		Title: "子方向", Description: "d", ParentID: in.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateIntent(child.ID, func(i *model.Intent) {
		i.Status = model.StatusPending
	}); err != nil {
		t.Fatal(err)
	}
	rec := call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/settle", map[string]any{
		"note": "父方向由已记录的事实收口", "fact_ids": []string{fact.ID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("settle = %d %s", rec.Code, rec.Body.String())
	}
	got, _ := s.Store.GetIntent(child.ID)
	if got.Status != model.StatusFailed || !got.Hold {
		t.Fatalf("child after settle = %+v, want failed+hold", got)
	}
	last := got.Notes[len(got.Notes)-1]
	if last.Kind != model.IntentNoteOrphan {
		t.Fatalf("child note = %q, want orphan", last.Kind)
	}
}

// A parent waiting on a child wakes when the commander declares that child done:
// a declared completion is a terminal result like any other.
func TestSettleChildWakesWaitingParent(t *testing.T) {
	s, child, fact := newSettleServer(t, model.StatusFailed, true, model.HoldActorCommander)
	parent, err := s.Store.CreateIntent(model.Intent{Title: "父方向", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateIntent(child.ID, func(i *model.Intent) { i.ParentID = parent.ID }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateIntent(parent.ID, func(i *model.Intent) {
		i.Status = model.StatusWaiting
		i.Wait = &model.Wait{Children: []string{child.ID}, Mode: model.WaitAll}
	}); err != nil {
		t.Fatal(err)
	}
	// The wake re-dispatches the parent, so stop it and let the case directory
	// close before the temp dir is removed (Windows keeps the run log open).
	t.Cleanup(func() {
		s.CancelAll()
		s.WaitIdle(5 * time.Second)
		time.Sleep(150 * time.Millisecond)
	})
	rec := call(t, s, http.MethodPost, "/api/intents/"+child.ID+"/settle", map[string]any{
		"note": "子方向已经拿到结果", "fact_ids": []string{fact.ID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("settle = %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := s.Store.GetIntent(parent.ID)
		return ok && got.Status != model.StatusWaiting
	})
	got, _ := s.Store.GetIntent(parent.ID)
	if got.Status == model.StatusWaiting {
		t.Fatalf("parent still waiting after the child was settled: %+v", got)
	}
	// The wake re-dispatches the parent, which is the observable result of a
	// settled child: a run now exists for it.
	started := false
	for _, run := range s.Store.Runs() {
		if run.IntentID == parent.ID {
			started = true
		}
	}
	if !started {
		t.Fatalf("parent %s was woken but never re-dispatched", parent.ID)
	}
}

// A parent kill must not become a way to seize the operator's stop: the guard
// runs in the same store transaction as the write, and the row keeps its holder.
func TestKillWorkerKeepsOperatorHold(t *testing.T) {
	f := newSpawnFixture(t)
	spawned := spawnBlockingWorker(t, f)
	if _, err := f.srv.CancelIntentBy(spawned.Intent.ID, "操作员叫停", model.HoldActorOperator); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := f.p.Store.GetIntent(spawned.Intent.ID)
		return ok && got.Hold && got.HeldBy == model.HoldActorOperator
	})
	// The row a parent could have seized before the guard moved under the lock:
	// stopped, not settled, still owned by the operator.
	if _, err := f.p.Store.UpdateIntent(spawned.Intent.ID, func(i *model.Intent) {
		i.Status = model.StatusPending
		i.Hold = true
		i.HeldBy = model.HoldActorOperator
		i.HoldReason = "操作员叫停"
	}); err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, f.srcID, http.MethodPost, "/api/workers/"+spawned.Intent.ID+"/kill", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "worker_held") {
		t.Fatalf("kill on operator-held worker = %d %s", rec.Code, rec.Body.String())
	}
	got, _ := f.p.Store.GetIntent(spawned.Intent.ID)
	if !got.Hold || got.HeldBy != model.HoldActorOperator || got.Status != model.StatusPending {
		t.Fatalf("kill changed the operator stop: %+v", got)
	}
}
