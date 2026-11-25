package api

// The receipt endpoint is what a closeout resends, so its success has to be
// idempotent: the second identical submission answers 200 and says so through a
// header, while a different result for the same action still conflicts. Store
// semantics are pinned in internal/store/action_idempotent_test.go; this file
// pins the HTTP mapping an executor actually sees.

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/store"
)

type silentExec struct{}

func (silentExec) Name() string { return "silent-fixture" }
func (silentExec) Run(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func TestIdenticalReceiptResubmissionAnswersSuccess(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, silentExec{}, dir, filepath.Join(dir, "cases"))
	t.Cleanup(func() { s.SetPaused(true); s.CancelAll(); s.WaitIdle(3 * time.Second) })

	in, err := st.CreateIntent(model.Intent{Title: "提交 c01 的 flag"})
	if err != nil {
		t.Fatal(err)
	}
	live, _, err := st.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	in = live
	rec := call(t, s, http.MethodPost, "/api/actions", map[string]string{
		"intent_id": in.ID, "idempotency_key": "flag-c01", "name": "提交 flag", "reference": "c01",
	})
	if rec.Code != 201 {
		t.Fatalf("begin=%d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Action model.ExternalAction `json:"action"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"version": created.Action.Version, "state": "completed", "result": "已提交 c01", "result_ref": "receipt:1"}
	if rec = call(t, s, http.MethodPost, "/api/actions/"+created.Action.ID+"/result", body); rec.Code != 200 {
		t.Fatalf("first resolve=%d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Tianwu-Duplicate-Result") != "" {
		t.Fatalf("first resolve was marked duplicate")
	}

	// The closeout resends the same receipt through the same (stale) version.
	rec = call(t, s, http.MethodPost, "/api/actions/"+created.Action.ID+"/result", body)
	if rec.Code != 200 {
		t.Fatalf("resubmission=%d %s, want 200 (a done action restated is not a conflict)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Tianwu-Duplicate-Result") != "1" {
		t.Fatalf("resubmission was not marked duplicate: %v", rec.Header())
	}
	if n := len(st.Actions(in.ID)); n != 1 {
		t.Fatalf("resubmission created %d action rows", n)
	}
	if got, _ := st.GetAction(created.Action.ID); got.Version != created.Action.Version+1 {
		t.Fatalf("version=%d, want one bump from the first resolve", got.Version)
	}
}
