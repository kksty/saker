package testutil

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/kksty/saker/internal/connectretry"
)

// Dial-level retry, for the few tests that cannot avoid a real loopback
// connection (WebSocket upgrade, SSE streaming, TLS handshake, "the port is
// closed" refusals). Those tests fail when the host drops the SYN, not when the
// code under test is wrong.
//
// The budget is connectretry's, the same one the production AOP client uses:
// a stalled connect is a property of the host, and a test harness that retried
// with its own smaller numbers failed on stalls the product already survived
// (2026-11-22: one full serial run red on a 3s terminal-bridge read while the
// host was in a stall burst). Two sets of numbers cannot be kept honest.
//
// The rule stays narrow:
//   - only connection-establishment timeouts are retried. A dial error means no
//     connection was established; once a request is written, any error is
//     reported to the test unchanged and never retried. A refusal is final: no
//     amount of waiting makes a listener appear.
//   - the waits and the whole phase are bounded by connectretry.Delays/Window;
//     a cancelled test context stops at once.
//   - every retry is recorded and reported; a rising count means the host is
//     sick, not the test.
var (
	retryMu  sync.Mutex
	retryLog []retryEvent
)

type retryEvent struct {
	addr string
	err  error
}

// newRetryDialContext wraps base (usually a net.Dialer.DialContext) with the
// shared retry budget. A nil base keeps net.Dialer's defaults.
func newRetryDialContext(base func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return retryDialContext(base, connectretry.Delays, connectretry.Window)
}

// retryDialContext is the retry with an explicit budget; the tests pass a short
// one so they do not wait out the production backoff.
func retryDialContext(base func(context.Context, string, string) (net.Conn, error), delays []time.Duration, window time.Duration) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	dial := base
	if dial == nil {
		dial = dialer.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err == nil {
			return conn, nil
		}
		// A cancelled deadline is the test's own contract, not a host fault;
		// retrying it would hide it. Neither is a refusal or a protocol answer.
		if ctx.Err() != nil || !isDialTimeout(err) {
			return nil, err
		}
		deadline := time.Now().Add(window)
		attempts := 1
		for _, delay := range delays {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				break
			}
			wait := delay
			if wait > remaining {
				wait = remaining
			}
			noteRetry(addr, err)
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, err
			case <-timer.C:
			}
			conn, err = dial(ctx, network, addr)
			attempts++
			if err == nil {
				return conn, nil
			}
			if ctx.Err() != nil || !isDialTimeout(err) {
				return nil, err
			}
		}
		return nil, fmt.Errorf("dial %s: %d connect attempts timed out (budget %s): %w",
			addr, attempts, budgetLabel(delays, window), err)
	}
}

// isDialTimeout reports whether a failed connect timed out on the wire instead
// of being answered. Same test as the production client's: only a connect whose
// handshake never completed is safe to repeat.
func isDialTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func budgetLabel(delays []time.Duration, window time.Duration) string {
	out := ""
	for i, d := range delays {
		if i > 0 {
			out += "/"
		}
		out += d.String()
	}
	return out + ", window " + window.String()
}

// ReportDialRetries logs every dial retry that happened from the moment it is
// called until the test ends, plus the count. Use it in tests that keep a real
// socket so a host problem is visible instead of silent.
func ReportDialRetries(t *testing.T) {
	t.Helper()
	start := len(retrySnapshot())
	t.Cleanup(func() {
		events := retrySnapshot()
		if start > len(events) {
			start = len(events)
		}
		for _, e := range events[start:] {
			t.Logf("dial retry: %s: %v", e.addr, e.err)
		}
		if n := len(events) - start; n > 0 {
			t.Logf("dial retries this test: %d (host loopback was not reliable; connection re-established before any request was sent)", n)
		}
	})
}

func noteRetry(what string, err error) {
	retryMu.Lock()
	retryLog = append(retryLog, retryEvent{addr: what, err: err})
	retryMu.Unlock()
	// Visible in the test binary's output even without -v, so a retry is never
	// silent.
	log.Printf("testutil: dial retry %s: %v", what, err)
}

// RetryCount is the number of dial retries recorded so far in this test binary.
func RetryCount() int {
	retryMu.Lock()
	defer retryMu.Unlock()
	return len(retryLog)
}

// Summary logs the package-wide retry count at the end of a test binary. Call it
// from TestMain after m.Run: a rising count means the host's loopback is sick,
// not that the tests are flaky.
func Summary() {
	if n := RetryCount(); n > 0 {
		log.Printf("testutil: %d dial retries in this package (host loopback was not reliable; each retry happened before any request was sent)", n)
	}
}

func retrySnapshot() []retryEvent {
	retryMu.Lock()
	defer retryMu.Unlock()
	out := make([]retryEvent, len(retryLog))
	copy(out, retryLog)
	return out
}

// RetryDial wraps a connect function with the narrow dial-level retry used by
// tests that keep a real socket. base may be nil for the standard dialer.
func RetryDial(base func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return newRetryDialContext(base)
}
