package decider

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/state"
)

// A restart recovery wake is the boot watchdog asking for one round without an
// operator click. It must be forced (the crash returned the interrupted
// directions to their old pending state, so the board can look unchanged and the
// attention gate would skip the re-planning round), and the decision log must say
// the machine asked for it — logging it as explicit_wake would attribute the
// automatic spending to the operator.
func TestRestartRecoveryWakeIsForcedAndLabelled(t *testing.T) {
	st := newStore(t)
	r := NewRunner(st, state.New(filepath.Join(t.TempDir(), "state.md")), "")
	r.LogPath = filepath.Join(t.TempDir(), "decisions.jsonl")
	r.ModelName, r.ContextWindow = "fixture", 1000000
	r.Session = func() RoundContext { return RoundContext{Resume: true} }
	r.Commander = func(context.Context, string, bool) (RoundResult, error) {
		return RoundResult{ContextTokens: 1000}, nil
	}
	if !isForced(wakeRestart) {
		t.Fatal("the restart recovery must be a forced wake")
	}
	d := r.Run(context.Background(), wakeRestart, isForced(wakeRestart))
	if d == nil {
		t.Fatal("a board that looks unchanged after the crash skipped the recovery round")
	}
	if len(d.Attention) != 1 || d.Attention[0].Code != "restart_recovery" {
		t.Fatalf("recovery attention = %+v, want one restart_recovery reason", d.Attention)
	}
}
