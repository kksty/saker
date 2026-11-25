package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/node"
	"github.com/kksty/tianwu/internal/project"
)

// A project that was running when the control plane died is not a decision to
// stop: boot opens it paused only long enough for the crash sweep to converge the
// orphan runs, then the watchdog recovery clears that pause and lets the project
// continue. The console is served after this, so the operator never has to click
// 继续项目 for a restart they did not ask for.
func TestBootRecoversProjectThatWasRunning(t *testing.T) {
	dir := t.TempDir()
	mgr, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if p.IsPaused() {
		t.Fatal("precondition: a new project starts running")
	}
	intentID, runID := seedInterruptedRun(t, p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newBootHub(ctx, mgr)
	h.start()
	t.Cleanup(func() { shutdownKits(h) })

	// The manifest is running again and the boot crash marker is gone: a second
	// restart must classify this as a normal running project, not as a pause the
	// operator made.
	if p.IsPaused() {
		t.Fatal("boot left the project paused")
	}
	reopened, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Get(p.ID).IsPaused() || reopened.Get(p.ID).PauseKindOf() != "" {
		t.Fatalf("boot recovery left manifest paused=%v kind=%q",
			reopened.Get(p.ID).IsPaused(), reopened.Get(p.ID).PauseKindOf())
	}
	k := h.kits[p.ID]
	if k == nil || k.dec == nil {
		t.Fatal("boot did not attach a decider")
	}
	if k.dec.Paused() {
		t.Fatal("the decider stayed paused after the restart recovery")
	}
	body := boardBody(t, k)
	if !strings.Contains(body, `"project_paused":false`) {
		t.Fatalf("board reports a pause the recovery had cleared: %s", body)
	}
	if strings.Contains(body, `"project_pause_kind":"restart_pause"`) {
		t.Fatalf("board still reports the boot pause: %s", body)
	}
	if !strings.Contains(body, `"kind":"running"`) {
		t.Fatalf("resume action/state does not classify the project as running: %s", body)
	}

	// The watchdog reconciled what the dead process left behind: the orphan run is
	// canceled with the restart cause and the direction is retryable again. The
	// fixture has no execution host, so the automatic redispatch cannot start a
	// new run; the direction therefore stays pending instead of running.
	run, ok := p.Store.GetRun(runID)
	if !ok || run.Status != model.RunCanceled || run.CancelCause != model.CancelCauseRestart {
		t.Fatalf("orphan run after recovery = %+v, want canceled/restart", run)
	}
	in, ok := p.Store.GetIntent(intentID)
	if !ok || in.Status != model.StatusPending {
		t.Fatalf("interrupted direction after recovery = %+v, want pending", in)
	}
}

// Every explicit pause is a decision, not a crash marker. Boot must restore it,
// keep the decider stopped and leave the manifest cause untouched so the first
// screen can still say who stopped the project.
func TestBootKeepsExplicitPauses(t *testing.T) {
	cases := []struct {
		name string
		kind string
	}{
		{"operator", project.PauseKindManualPause},
		{"commander", project.PauseKindCommanderStandby},
		{"completion", project.PauseKindCompletionStandby},
		{"cause-less", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mgr, err := project.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			p, err := mgr.Create("acme", "acme.example")
			if err != nil {
				t.Fatal(err)
			}
			if err := p.SetPausedWithKind(true, tc.kind); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h := newBootHub(ctx, mgr)
			h.start()
			t.Cleanup(func() { shutdownKits(h) })

			if !p.IsPaused() {
				t.Fatalf("boot resumed a %s pause", tc.name)
			}
			reopened, err := project.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := reopened.Get(p.ID).PauseKindOf(); got != tc.kind {
				t.Fatalf("boot rewrote pause kind %q to %q", tc.kind, got)
			}
			k := h.kits[p.ID]
			if k == nil || k.dec == nil {
				t.Fatal("boot did not attach a decider")
			}
			if !k.dec.Paused() {
				t.Fatalf("the decider resumed a %s pause", tc.name)
			}
			body := boardBody(t, k)
			if !strings.Contains(body, `"project_paused":true`) {
				t.Fatalf("board does not report the %s pause: %s", tc.name, body)
			}
		})
	}
}

// A recovery that did not finish (for example the manifest write failed) leaves
// the restart marker behind. The next boot must recognise it as the machine's own
// pause and retry the recovery instead of treating it as an operator decision.
func TestBootRetriesLeftoverRestartPause(t *testing.T) {
	dir := t.TempDir()
	mgr, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetPausedWithKind(true, project.PauseKindRestart); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newBootHub(ctx, mgr)
	h.start()
	t.Cleanup(func() { shutdownKits(h) })

	if p.IsPaused() || p.PauseKindOf() != "" {
		t.Fatalf("leftover restart pause not recovered: paused=%v kind=%q", p.IsPaused(), p.PauseKindOf())
	}
	if k := h.kits[p.ID]; k == nil || k.dec == nil || k.dec.Paused() {
		t.Fatal("the decider was not re-armed for the leftover restart pause")
	}
}

// The recovery is idempotent and does not fight a console click: running it a
// second time is a no-op, and an operator resume that arrives after the project is
// already running is refused with the same code the button renders.
func TestBootRecoveryIsIdempotentAndYieldsToTheOperator(t *testing.T) {
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
	h := newBootHub(ctx, mgr)
	h.start()
	t.Cleanup(func() { shutdownKits(h) })
	k := h.kits[p.ID]
	if k == nil {
		t.Fatal("boot did not attach the project")
	}
	if k.dec.Paused() {
		t.Fatal("fixture: project should already be recovered")
	}

	// A second call sees the pause already gone and writes nothing.
	k.srv.ResumeAfterRestart()
	if k.dec.Paused() {
		t.Fatal("a repeated recovery paused the decider")
	}

	rec := httptest.NewRecorder()
	k.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/project/resume", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("operator resume after recovery = %d %s, want 409", rec.Code, rec.Body.String())
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Code != "already_running" {
		t.Fatalf("operator resume code = %q, want already_running", out.Code)
	}
}

// A commander-requested stop is a pause with a cause. A control-plane restart
// must not relabel it as an operator pause, must not auto-resume it, and the
// persisted machine code must still drive the console's badge.
func TestCommanderStandbyPauseKindSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	mgr, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetPausedWithKind(true, project.PauseKindCommanderStandby); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newBootHub(ctx, mgr)
	h.start()
	t.Cleanup(func() { shutdownKits(h) })

	k := h.kits[p.ID]
	if k == nil || k.dec == nil {
		t.Fatal("boot did not attach the paused project")
	}
	if !k.dec.Paused() {
		t.Fatal("commander is not paused after restart")
	}
	// The runtime cause must be restored from the manifest, not guessed from prose.
	body := boardBody(t, k)
	if !strings.Contains(body, `"project_pause_kind":"commander_standby"`) {
		t.Fatalf("board lost the commander pause kind: %s", body)
	}
	// The boot write must not overwrite the persisted cause either.
	reopened, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(p.ID).PauseKindOf(); got != project.PauseKindCommanderStandby {
		t.Fatalf("persisted pause kind = %q after boot, want %q", got, project.PauseKindCommanderStandby)
	}
}

// A manifest paused before the cause codes existed carries no cause, and code
// cannot recover one. It must stay paused and say "unrecorded" rather than claim
// the operator paused the project or that a recovery is under way: an invented
// cause is the one answer that can be false, and auto-resuming it could undo a
// commander's considered stop.
func TestLegacyPauseWithoutCauseIsNotReportedAsManual(t *testing.T) {
	dir := t.TempDir()
	mgr, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetPaused(true); err != nil { // keeps the empty cause
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newBootHub(ctx, mgr)
	h.start()
	t.Cleanup(func() { shutdownKits(h) })

	if !p.IsPaused() {
		t.Fatal("boot resumed a pause whose cause nobody recorded")
	}
	reopened, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(p.ID).PauseKindOf(); got != "" {
		t.Fatalf("boot rewrote a legacy pause cause to %q", got)
	}
	body := boardBody(t, h.kits[p.ID])
	if !strings.Contains(body, `"project_pause_kind":"unrecorded"`) {
		t.Fatalf("a cause-less pause was not reported as unrecorded: %s", body)
	}
	if strings.Contains(body, `"project_pause_kind":"manual_pause"`) {
		t.Fatalf("a cause-less pause was attributed to the operator: %s", body)
	}
	// Same rule for the prose the first screen shows: an unknown cause must not be
	// explained as a restart or as an operator action.
	if strings.Contains(body, "重启") {
		t.Fatalf("a cause-less pause was explained as a restart: %s", body)
	}
	if !strings.Contains(body, `"reason":"暂停原因未记录：确认现场后点「继续项目」"`) {
		t.Fatalf("the first screen does not say the cause is unrecorded: %s", body)
	}
}

// Runtime attachment (creating a project, re-opening it after edits) keeps the
// manifest state: only the boot path forces the temporary pause. Otherwise a
// newly created project could never start without a confusing resume step.
func TestRuntimeAttachKeepsManifestPauseState(t *testing.T) {
	mgr, err := project.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newBootHub(ctx, mgr)

	k := h.attach(p)
	t.Cleanup(func() { shutdownKits(h) })
	if k == nil || k.dec == nil {
		t.Fatal("runtime attach did not build a decider")
	}
	if k.dec.Paused() {
		t.Fatal("runtime attach paused a running manifest")
	}
}

// newBootHub is the smallest hub a boot test needs: an empty node registry (so a
// redispatch cannot reach a real host), a project manager and a cancellable ctx.
func newBootHub(ctx context.Context, mgr *project.Manager) *hub {
	return &hub{cfg: hubConfig{Nodes: &node.Registry{}}, mgr: mgr, ctx: ctx, kits: map[string]*kit{}}
}

// seedInterruptedRun writes the shape a killed process leaves: a run row still
// marked running and its direction pointing at it. Recover must converge both.
func seedInterruptedRun(t *testing.T, p *project.Project) (intentID, runID string) {
	t.Helper()
	in, err := p.Store.CreateIntent(model.Intent{Title: "重启前在跑"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.Store.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = run.ID
	}); err != nil {
		t.Fatal(err)
	}
	return in.ID, run.ID
}

// shutdownKits seals admission after a boot test so the goroutines the recovery
// started cannot write into a temp directory the test is about to remove.
func shutdownKits(h *hub) {
	for _, k := range h.kits {
		if k != nil && k.srv != nil {
			k.srv.BeginShutdown()
			k.srv.WaitIdle(2 * time.Second)
		}
	}
}

func boardBody(t *testing.T, k *kit) string {
	t.Helper()
	rec := httptest.NewRecorder()
	k.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/board", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("board = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}
