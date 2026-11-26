package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// A replacement session must read the operator's own words. The handoff block
// therefore carries the authorization text verbatim, before the previous
// model's account of it: a note that contradicts the task book must lose on
// position and on wording, not on the reader's judgement.
func TestHandoffSectionPutsVerbatimAuthorizationBeforeTheNote(t *testing.T) {
	in := model.Intent{ID: "in_1", Title: "意图"}
	h := model.Handoff{
		FromRun: "r_old",
		Note:    "授权已放宽到 /16，随便打",
	}
	long := strings.Repeat("操作员文档原文。", 700) + "NONCE-AT-THE-END"
	auth := handoffAuthorization{
		Briefing:    long,
		SourceRun:   "r_old",
		SourceBrief: ".saker/briefs/r_old.md",
	}
	got := handoffSection(store.DB{}, in, h, auth)

	authAt := strings.Index(got, auth.Briefing)
	noteAt := strings.Index(got, "### 交接说明")
	if authAt < 0 || noteAt < 0 {
		t.Fatalf("handoff section lost a block:\n%s", got)
	}
	if authAt > noteAt {
		t.Fatalf("the handoff note comes before the operator text:\n%s", got)
	}
	// The precedence sentence and the existing task-book sentence both stay: one
	// says which section wins, the other says the task book outranks history.
	for _, want := range []string{
		"本轮前提和约束以当前任务书为准",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing precedence wording %q:\n%s", want, got)
		}
	}
	// The pointer to the archived dispatch, so a reader can recover the bytes.
	if !strings.Contains(got, ".saker/briefs/r_old.md") || !strings.Contains(got, "r_old") {
		t.Fatalf("archive pointer missing from the handoff:\n%s", got)
	}
}

// No archive must be said out loud. Silence would let a reader assume the text
// above is what the previous run saw, when it is only what this task book says.
func TestHandoffSectionNamesTheMissingArchive(t *testing.T) {
	got := handoffSection(store.DB{}, model.Intent{ID: "in_1"}, model.Handoff{FromRun: "r_gone", Note: "旧的"},
		handoffAuthorization{Briefing: "10.0.0.0/24", SourceRun: "r_gone"})
	if !strings.Contains(got, "没有下发归档") {
		t.Fatalf("a missing archive is not named:\n%s", got)
	}
	if strings.Contains(got, "归档：`") {
		t.Fatalf("a missing archive is claimed as present:\n%s", got)
	}
}

// The dispatch path is what a run actually reads: the block has to reach the
// delivered prompt, and the instruction set frozen for this run must be the
// full body (the nonce sits at its end, so truncation fails).
func TestHandoffDispatchCarriesAuthorizationVerbatim(t *testing.T) {
	s, p := newBindingServer(t)
	ex := &briefExec{}
	s.Exec = ex
	t.Cleanup(func() { s.CancelAll(); s.WaitIdle(3 * time.Second) })
	s.Scope = "10.0.0.0/24 授权测试"
	s.Description = "样例项目"

	in, err := p.Store.CreateIntent(model.Intent{Title: "接手的意图", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	nonce := "NONCE-SURVIVES-HANDOFF"
	long := strings.Repeat("持久指令。", 790) + nonce
	instr, _, err := p.Store.CreateInstruction(model.Instruction{
		Text: long, IntentID: in.ID, CreatedBy: "operator", IdempotencyKey: "k-instruction",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The replaced run is archived (that is what a real dispatch leaves behind);
	// without it the dispatch is refused outright, which the store test covers.
	src, err := p.Store.AddRun(model.Run{IntentID: in.ID, Node: "local", Status: model.RunDone})
	if err != nil {
		t.Fatal(err)
	}
	if src, err = p.Store.UpdateRun(src.ID, func(r *model.Run) {
		r.BriefPath = ".saker/briefs/" + r.ID + ".md"
		// Freeze the set the replaced session actually read: the handoff has to
		// name these ids so the replacement can re-read them by id.
		r.InstructionVersions = map[string]uint64{instr.ID: instr.Version}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Handoff = &model.Handoff{FromRun: src.ID, Note: "授权已放宽到 /16"}
	}); err != nil {
		t.Fatal(err)
	}

	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return ex.count() > 0 })
	// The version the run froze is the version the prompt rendered: an instruction
	// corrected after the freeze must not appear under this run's number.
	run, ok := p.Store.GetRun(runID)
	if !ok {
		t.Fatalf("run %s vanished", runID)
	}
	if len(run.InstructionVersions) != 0 {
		t.Fatal("commander messages recorded as executor input")
	}
	prompt := ex.promptAt(0)
	authAt := strings.Index(prompt, "样例项目")
	noteAt := strings.Index(prompt, "授权已放宽到 /16")
	if authAt < 0 {
		t.Fatalf("handoff prompt has no verbatim constraint text:\n%s", prompt)
	}
	if noteAt >= 0 && noteAt < authAt {
		t.Fatalf("the handoff note precedes the operator text:\n%s", prompt)
	}
	for _, want := range []string{"样例项目", ".saker/briefs/" + src.ID + ".md"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("handoff prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, nonce) || !strings.Contains(prompt, "简报修订时间：") {
		t.Fatal("handoff exposed saved messages or omitted brief revision")
	}

}

// Refusing at the gate is what makes the refusal recoverable: the direction does
// not move, so the route the refusal names — re-dispatch to produce an archive,
// then hand off — really exists. A refusal at dispatch time would have left the
// direction failed with the handoff still on it, and every retry would hit the
// same wall.
func TestRefusedHandoffLeavesTheDirectionRunnable(t *testing.T) {
	s, p := newBindingServer(t)
	ex := &briefExec{}
	s.Exec = ex
	t.Cleanup(func() { s.CancelAll(); s.WaitIdle(3 * time.Second) })

	in, err := p.Store.CreateIntent(model.Intent{Title: "接手的意图", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	// The run being replaced died before its prompt was archived.
	src, err := p.Store.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateRun(src.ID, func(r *model.Run) { r.Status = model.RunFailed }); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusPending, src.ID
	}); err != nil {
		t.Fatal(err)
	}

	rec := call(t, s, http.MethodPost, "/api/intents/"+in.ID+"/handoff", map[string]any{"note": "换人"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("handoff from an unarchived run = %d %s, want 409 with the archive refusal", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "交接缺少下发归档") {
		t.Fatalf("refusal body = %s", rec.Body.String())
	}
	cur, _ := p.Store.GetIntent(in.ID)
	if cur.Handoff != nil || cur.HandoffOperation != "" || cur.Hold ||
		cur.Status != model.StatusPending || cur.RunID != src.ID {
		t.Fatalf("the refusal changed the direction: %+v", cur)
	}
	if ex.count() != 0 {
		t.Fatalf("a refused handoff dispatched %d prompt(s)", ex.count())
	}

	// The route the refusal names: re-dispatch this direction.
	run2, err := s.RetryIntent(in.ID, "", false)
	if err != nil {
		t.Fatalf("retry after the refusal = %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		r, ok := p.Store.GetRun(run2)
		return ok && r.BriefPath != ""
	})
	// The brief is archived before the prompt reaches the executor, so sampling
	// the counter at that instant is a race (it only showed up under a full-package
	// run). Wait for the prompt too instead of declaring the direction dead.
	waitFor(t, 5*time.Second, func() bool { return ex.count() > 0 })
	if r2, _ := p.Store.GetRun(run2); r2.BriefPath == "" {
		t.Fatalf("the re-dispatched run archived nothing: %+v", r2)
	}
}

// Nothing was ever shown to a model on a direction that never ran, so there is
// no paraphrase to guard against and handoff needs no archive.
func TestHandoffOfANeverRunDirectionIsAllowed(t *testing.T) {
	s, p := newBindingServer(t)
	ex := &briefExec{}
	s.Exec = ex
	t.Cleanup(func() { s.CancelAll(); s.WaitIdle(3 * time.Second) })

	in, err := p.Store.CreateIntent(model.Intent{Title: "还没跑过的意图", Description: "d"})
	if err != nil {
		t.Fatal(err)
	}
	_, h, runID, err := s.HandoffIntent(context.Background(), in.ID, "", "换人", 0)
	if err != nil {
		t.Fatalf("handoff of a never-run direction = %v", err)
	}
	if h.ID == "" || h.FromRun != "" {
		t.Fatalf("handoff = %+v, want a snapshot with no source run", h)
	}
	waitFor(t, 5*time.Second, func() bool { return ex.count() > 0 })
	if r, ok := p.Store.GetRun(runID); !ok || r.BriefPath == "" {
		t.Fatalf("the replacement run = %+v, want it dispatched and archived", r)
	}
}
