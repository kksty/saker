package api

import (
	"testing"
	"time"

	"github.com/kksty/saker/internal/testutil"
)

// livenessWait bounds the "it has to happen at all" waits in this package's
// tests: a host has to see a frame, a native command has to start, a goroutine
// has to finish. It is a liveness guard, not the assertion — the test still
// fails when the event never happens — so a bound tight enough to be crossed by
// scheduling noise only produces false reds.
//
// Three seconds was crossed for real: this suite runs parallel and shares the
// host with acceptance runs, and one red landed at 3.07s while the command was
// merely queued. A false red costs a re-run plus somebody's judgement; the bound
// here costs nothing when the event happens on time.
//
// Ten seconds was crossed for real too: on 2026-11-22 the gate failed
// TestOccupancyRemainsReadableAndCannotBeReturnedDuringNativeOperation at 10.07s
// with "native command did not start" — the bound itself, not the assertion. The
// same host drops outbound SYNs for 2–12s (measured that day; see
// terminal_test.go's 1.01s red for the same symptom on a fixture connect), so a
// 10s bound sits inside the stall band and will keep producing false reds.
//
// The bound must also outlive the fixture connect budget it wraps:
// testutil.FixtureWindow is 20s, so a fixture dial that keeps retrying inside its
// own budget is not declared dead here first. Lower this below that window and
// the liveness guard starts cutting retries short.
const livenessWait = 30 * time.Second

// TestLivenessWaitOutlivesFixtureWindow pins the relation the comment above
// relies on: a fixture connect may spend up to testutil.FixtureWindow retrying
// on this host, and the liveness guard must not cut that budget short. Without
// this, lowering the constant back to 10s looks like a harmless tweak.
func TestLivenessWaitOutlivesFixtureWindow(t *testing.T) {
	if livenessWait <= testutil.FixtureWindow {
		t.Fatalf("livenessWait=%s must exceed testutil.FixtureWindow=%s", livenessWait, testutil.FixtureWindow)
	}
}
