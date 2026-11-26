package testutil

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/kksty/saker/internal/connectretry"
)

// The fixture-side connect budget: one number for every test that opens a real
// loopback socket.
//
// It is deliberately wider than production's connectretry.Window. A fixture
// server is local, so waiting longer costs a test some seconds, while a host
// stall of 1–12s (measured 2026-11-22, idle machine included) fails the test for
// a reason that has nothing to do with the code under test. "Run it in a quiet
// window" is not a strategy: the stalls happen at 10% CPU with no load.
//
// What must not happen is each call site inventing its own 1s/2s/3s budget —
// that is how a suite kept dying at 1.01s while the same failure was being
// retried for 8s one package over. Waits come from connectretry.Delays, the
// retryable-error test from connectretry.IsTimeout.
// FixtureWindow is the whole budget for one fixture connect or open. It is a
// variable only so the helper's own test can shorten it; nothing else assigns
// it.
var FixtureWindow = 20 * time.Second

// FixtureAttempt bounds one attempt inside that window (a dial plus the first
// frame a bridge must answer with).
const FixtureAttempt = 3 * time.Second

// FixtureContext is the context a fixture operation should carry: the whole
// budget, not a per-station number.
func FixtureContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), FixtureWindow)
}

// RetryConnect runs fn until it succeeds, a non-connect failure appears, or the
// fixture budget runs out, retrying only when the TCP connect itself never
// happened. That is the one case docs/01-architecture.md §9 allows: the host
// occasionally drops the SYN, so connect() fails after ~1s even though the
// listener is healthy, and no request was ever sent.
//
// Anything past the connect — a refused handshake, a 401, a broken stream — is
// returned to the caller unchanged on the first attempt: retrying that would
// hide a real failure.
//
// Every retry is recorded in the same counter as the transport-level retry, so a
// package whose TestMain calls Summary() prints one count line at the end: a
// retry never stays silent even when the test passes.
func RetryConnect[T any](t *testing.T, what string, fn func() (T, error)) (T, error) {
	t.Helper()
	return RetryConnectWhen(t, what, fn, isDialFailure)
}

// RetryConnectWhen is RetryConnect with the caller supplying the test for "the
// connection was never established".
//
// It exists for the case where the failing dial sits behind an interface that
// reports the failure as a value instead of an error: the terminal bridge, for
// example, sends the host's connection error as its greeting frame, so by the
// time the test sees it there is no *net.OpError left to inspect. The caller
// turns that value back into an error and says how to recognise it — and the
// shared transport test (IsConnectTimeout) belongs in that predicate, because a
// dropped SYN and a fatal greeting frame are the same "the bridge is not up yet".
//
// A non-connect failure — a refusal, a wrong greeting, a protocol error — is
// returned unchanged on the first attempt.
func RetryConnectWhen[T any](t *testing.T, what string, fn func() (T, error), dialFailed func(error) bool) (T, error) {
	t.Helper()
	deadline := time.Now().Add(FixtureWindow)
	var zero T
	var last error
	for attempt := 1; ; attempt++ {
		value, err := fn()
		if err == nil {
			return value, nil
		}
		last = err
		if !dialFailed(err) {
			return zero, err
		}
		wait := connectretry.WaitForNext(attempt, deadline)
		if wait <= 0 {
			t.Logf("connect retry for %s gave up after %d attempts inside %s: %v (reported as-is)",
				what, attempt, FixtureWindow, last)
			return zero, last
		}
		noteRetry(what, err)
		t.Logf("connect retry %d for %s in %s: %v", attempt, what, wait.Round(time.Millisecond), err)
		time.Sleep(wait)
	}
}

// IsConnectTimeout is the fixture-side spelling of the production predicate:
// the host dropped the SYN, so the connect never completed and nothing was sent.
// Call sites that reconstruct the failure as a value (the terminal bridge turns
// it into a fatal greeting frame) must put this in their own predicate, or the
// one failure the budget exists for is the one they fail on first.
func IsConnectTimeout(err error) bool { return connectretry.IsTimeout(err) }

// isDialFailure reports whether err came from establishing the TCP connection,
// as opposed to anything that happened after it existed.
//
// It is wider than the production rule on purpose: production retries only a
// timeout, because a refusal means the listener is absent and waiting will not
// bring it back, while a fixture server may simply not be listening yet when the
// first attempt fires and then appear a moment later. Both sides still agree on
// the shape that matters — nothing was sent, so a repeat cannot duplicate a
// request. A fixture pointed at a genuinely dead port therefore burns the window
// instead of failing at once; that cost is confined to tests and is the price of
// not encoding "the server has started" as a sleep.
func isDialFailure(err error) bool {
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return false
	}
	return opErr.Op == "dial"
}
