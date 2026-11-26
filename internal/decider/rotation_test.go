package decider

// Post-handoff session rotation (P0-8), built in. These tests pin the whole
// contract: only a valid handoff receipt arms it, the decision is a one-shot
// in-memory fact of this Runner (it survives neither a restart nor a failed
// attempt), the prompt and the actual session call get the same answer, the new
// session receives the full note with its own preamble, and rotating resets the
// pressure cycle so the next climb hands off again.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/state"
	"github.com/kksty/saker/internal/store"
)

const rotationNote = "唯一标记-ROTATED-NOTE：下一步核对 /admin 入口"

// rotationTranscript records what the runner told the model (prompt) and what it
// asked the harness session layer to do (resume), so a test can assert the two
// agree. A finite scripted sequence stands in for the harness commander.
type rotationTranscript struct {
	session RoundContext
	prompts []string
	resumes []bool
	failOn  int // 1-based commander call that returns an error; 0 disables
	calls   int
}

// newRotationRunner builds a runner whose round 1 climbs to the 85% line and
// whose round 2 is the handoff round. writeHandoffNote=false makes it the
// missing-receipt failure mode instead of a valid handoff.
func newRotationRunner(t *testing.T, st *store.Store, mem *state.File, writeHandoffNote bool) (*Runner, *rotationTranscript) {
	t.Helper()
	tr := &rotationTranscript{}
	r := NewRunner(st, mem, "")
	r.ModelName, r.ContextWindow = "fixture", 128000
	r.Session = func() RoundContext { return tr.session }
	r.Commander = func(ctx context.Context, prompt string, resume bool) (RoundResult, error) {
		tr.calls++
		tr.prompts = append(tr.prompts, prompt)
		tr.resumes = append(tr.resumes, resume)
		if tr.failOn > 0 && tr.calls == tr.failOn {
			return RoundResult{}, errors.New("fixture：新会话启动失败")
		}
		switch tr.calls {
		case 1:
			return RoundResult{ContextTokens: 110000}, nil
		case 2:
			if !writeHandoffNote {
				return RoundResult{ContextTokens: 110000}, nil
			}
			return RoundResult{ContextTokens: 111000, Actions: []Action{{Kind: "state"}},
				RunID: completeNoteRound(t, st, ctx, rotationNote)}, nil
		default:
			return RoundResult{ContextTokens: 1000}, nil
		}
	}
	return r, tr
}

// run executes one round with the caller-supplied session answer.
func (tr *rotationTranscript) run(t *testing.T, r *Runner, rc RoundContext) *Decision {
	t.Helper()
	tr.session = rc
	return r.Run(context.Background(), "manual", true)
}

func resumedSession() RoundContext { return RoundContext{Resume: true} }

func hasRotationPrompt(p string) bool { return strings.Contains(p, "计划内的新会话第一轮") }

// driveToPendingRotation climbs to 85%, runs the verified handoff, and leaves
// the armed pendingRotate behind. It returns the handoff decision.
func driveToPendingRotation(t *testing.T, r *Runner, tr *rotationTranscript) *Decision {
	t.Helper()
	tr.run(t, r, resumedSession())
	handoff := tr.run(t, r, resumedSession())
	if handoff.HandoffReceipt == nil {
		t.Fatalf("fixture handoff has no valid receipt: %+v", handoff)
	}
	if !r.pendingRotate {
		t.Fatal("verified handoff did not arm the planned rotation")
	}
	return handoff
}

func TestRotationStartsFreshSessionAfterVerifiedHandoff(t *testing.T) {
	st := newStore(t)
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	r, tr := newRotationRunner(t, st, mem, true)
	driveToPendingRotation(t, r, tr)

	rotated := tr.run(t, r, resumedSession())
	if !tr.resumes[0] || !tr.resumes[1] {
		t.Fatalf("the rounds before rotation must resume the session, got %v", tr.resumes[:2])
	}
	if tr.resumes[2] {
		t.Fatal("the rotation round asked the harness to resume the old session")
	}
	p := tr.prompts[2]
	for _, want := range []string{"计划内的新会话第一轮", "不是会话丢失", "先读 board 与账本", "不要照笔记执行"} {
		if !strings.Contains(p, want) {
			t.Fatalf("rotation prompt lost its planned-new-session contract (%q):\n%s", want, p)
		}
	}
	if !strings.Contains(p, StateFullHeader) || !strings.Contains(p, rotationNote) {
		t.Fatalf("the rotation round did not inject the full note:\n%s", p)
	}
	if !hasNote(rotated.Notes, "轮换") {
		t.Fatalf("the session swap is not auditable in the decision log: %v", rotated.Notes)
	}
	// The rotation note names the round's entry points. It must not describe them
	// as an authorization boundary, and it must name the project documents — with
	// no assertion here, this wording had no guard at all.
	joined := strings.Join(rotated.Notes, "\n")
	if !strings.Contains(joined, "board 与项目文档") || strings.Contains(joined, "授权") {
		t.Fatalf("rotation note entry points are wrong: %v", rotated.Notes)
	}
	if r.pendingRotate {
		t.Fatal("pendingRotate was not consumed by the rotation round")
	}
	if r.handoffDone || r.handoffForced || r.handoffBypassed || r.handoffMissing != 0 {
		t.Fatalf("rotation did not reset the pressure cycle: done=%v forced=%v bypassed=%v missing=%d",
			r.handoffDone, r.handoffForced, r.handoffBypassed, r.handoffMissing)
	}
}

// A round whose session is already gone has nothing to rotate: the one-shot is
// cleared and the ordinary "session lost" full-note contract stays.
func TestPlannedRotationIsDroppedWhenSessionAlreadyLost(t *testing.T) {
	st := newStore(t)
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	r, tr := newRotationRunner(t, st, mem, true)
	driveToPendingRotation(t, r, tr)

	lost := tr.run(t, r, RoundContext{Resume: false})
	if hasRotationPrompt(tr.prompts[2]) {
		t.Fatalf("a lost session was described as a planned rotation:\n%s", tr.prompts[2])
	}
	if !strings.Contains(tr.prompts[2], "你没有上一轮的会话记忆") {
		t.Fatalf("lost-session wording disappeared:\n%s", tr.prompts[2])
	}
	if !strings.Contains(tr.prompts[2], rotationNote) {
		t.Fatalf("the lost-session round did not inject the full note:\n%s", tr.prompts[2])
	}
	if r.pendingRotate {
		t.Fatal("a lost session must clear the one-shot rotation instead of keeping it")
	}
	if hasNote(lost.Notes, "轮换") {
		t.Fatalf("nothing was rotated but the log says it was: %v", lost.Notes)
	}
}

// pendingRotate is process memory only: a restart keeps the long session and
// does not rotate from a persisted Decision/receipt.
func TestPendingRotationDoesNotSurviveRestart(t *testing.T) {
	st := newStore(t)
	r1, tr1 := newRotationRunner(t, st, state.New(filepath.Join(t.TempDir(), "state.md")), true)
	driveToPendingRotation(t, r1, tr1)

	// New process: a fresh Runner with the same ledger and no in-memory flags.
	r2, tr2 := newRotationRunner(t, st, state.New(filepath.Join(t.TempDir(), "state2.md")), true)
	r2.lastContext = 110000 // what seedContext recovers after a restart
	if r2.pendingRotate {
		t.Fatal("pendingRotate came back after a restart")
	}
	d := tr2.run(t, r2, resumedSession())
	if !tr2.resumes[0] || hasRotationPrompt(tr2.prompts[0]) {
		t.Fatalf("restart rotated the session (resume=%v):\n%s", tr2.resumes[0], tr2.prompts[0])
	}
	if hasNote(d.Notes, "轮换") {
		t.Fatalf("restart recorded a rotation it did not perform: %v", d.Notes)
	}
}

// No valid receipt means the pressure cycle never closed: one missing attempt
// keeps the handoff armed, and the second releases the pressure. Neither arms a
// rotation.
func TestRotationNeedsValidHandoffReceipt(t *testing.T) {
	st := newStore(t)
	r, tr := newRotationRunner(t, st, state.New(filepath.Join(t.TempDir(), "state.md")), false)
	tr.run(t, r, resumedSession()) // climb to the 85% line
	firstMissing := tr.run(t, r, resumedSession())
	if !firstMissing.Handoff || firstMissing.HandoffReleased {
		t.Fatalf("first missing attempt must stay a handoff round below the cap: %+v", firstMissing)
	}
	if r.pendingRotate {
		t.Fatal("a handoff without a receipt armed rotation")
	}
	secondMissing := tr.run(t, r, resumedSession())
	if !secondMissing.Handoff || !secondMissing.HandoffReleased {
		t.Fatalf("second missing attempt must release the pressure: %+v", secondMissing)
	}
	if r.pendingRotate {
		t.Fatal("a bypassed handoff armed rotation")
	}
	released := tr.run(t, r, resumedSession())
	if released.Handoff {
		t.Fatal("bypassed cycle asked for a third handoff")
	}
	if hasRotationPrompt(tr.prompts[3]) || hasNote(released.Notes, "轮换") {
		t.Fatalf("a bypassed handoff rotated the session:\n%s", tr.prompts[3])
	}
	for i, ok := range tr.resumes {
		if !ok {
			t.Fatalf("round %d resumed=false without a valid handoff", i+1)
		}
	}
}

// A forced repair handoff (compaction before any handoff) is still a handoff
// round; when it writes no valid receipt it must not rotate the session.
func TestForcedHandoffWithoutReceiptDoesNotRotate(t *testing.T) {
	st := newStore(t)
	r, tr := newRotationRunner(t, st, state.New(filepath.Join(t.TempDir(), "state.md")), false)
	r.lastContext, r.handoffForced = 110000, true
	d := tr.run(t, r, resumedSession())
	if !d.Handoff {
		t.Fatalf("fixture did not enter the forced repair handoff: %+v", d)
	}
	if !strings.Contains(tr.prompts[0], "在交接完成前压缩了上下文") {
		t.Fatalf("forced repair wording missing:\n%s", tr.prompts[0])
	}
	if r.pendingRotate || hasRotationPrompt(tr.prompts[0]) || hasNote(d.Notes, "轮换") {
		t.Fatal("a failed forced handoff armed rotation")
	}
	if !tr.resumes[0] {
		t.Fatal("forced repair round changed the session")
	}
}

// A rotation starts a new pressure cycle: climbing above the handoff line again
// must schedule a new handoff round (otherwise the note would never be rewritten
// in the new session).
func TestRotationResetsThePressureCycle(t *testing.T) {
	st := newStore(t)
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	r, tr := newRotationRunner(t, st, mem, true)
	driveToPendingRotation(t, r, tr)
	tr.run(t, r, resumedSession()) // the rotated round

	r.lastContext = 110000
	next := tr.run(t, r, resumedSession())
	if !next.Handoff || !strings.Contains(tr.prompts[3], "## 交接（本轮必做）") {
		t.Fatalf("the pressure cycle was not reset after rotation:\n%s", tr.prompts[3])
	}
}

// The receipt closes one cycle and the rotation is consumed by one round only.
// A failed new-session round is not retried: the following wake resumes the
// session it actually started, and never rotates a second time from the same
// receipt.
func TestRotationIsNotRetriedAfterFailedNewSessionRound(t *testing.T) {
	st := newStore(t)
	mem := state.New(filepath.Join(t.TempDir(), "state.md"))
	r, tr := newRotationRunner(t, st, mem, true)
	driveToPendingRotation(t, r, tr)

	tr.failOn = 3
	failed := tr.run(t, r, resumedSession())
	if failed.Error == "" {
		t.Fatal("fixture did not fail the rotation round")
	}
	if tr.resumes[2] {
		t.Fatal("the failed rotation round did not ask for a fresh session")
	}
	if r.pendingRotate {
		t.Fatal("a failed rotation round left the one-shot armed for a retry")
	}
	retry := tr.run(t, r, resumedSession())
	if !tr.resumes[3] {
		t.Fatal("the retry rotated again from the same handoff receipt")
	}
	if hasRotationPrompt(tr.prompts[3]) || hasNote(retry.Notes, "轮换") {
		t.Fatalf("the retry repeated the rotation:\n%s", tr.prompts[3])
	}
}
