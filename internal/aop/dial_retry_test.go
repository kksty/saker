package aop

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	pb "github.com/kksty/saker/internal/aop/pb"
	"github.com/kksty/saker/internal/connectretry"
	"github.com/kksty/saker/internal/ws"
	"google.golang.org/protobuf/types/known/anypb"
)

// The connect phase retries a *timeout* on a fixed schedule inside a fixed
// window. The reason is measured, not guessed: on 2026-11-22 this host dropped
// the SYN of new connections for 1–12s at a time (every new connect on v4
// loopback, v6 loopback, the production hub and a LAN address failed together,
// while an echo on an established socket ran 5,854 round trips with a 1.5ms
// worst case). A dropped connect costs a whole round with zero events
// (2026-11-21: five commander rounds), and the old single 300ms retry still
// lost every stall longer than that.
//
// These tests pin the boundaries that keep the retry from becoming a lie:
// timeouts recover, a dead host still fails inside the window, a refusal is not
// retried, a cancelled caller is not retried, and the budget itself is the
// agreed one (0.3/1/2/4s, 8s).
func TestDialRecoversAfterTransientConnectTimeouts(t *testing.T) {
	hub := startTestHub(t, func(*testHub, *ws.Conn, *pb.Envelope, *anypb.Any) {})
	defer hub.server.Close()
	defer setDialScheduleForTest([]time.Duration{time.Millisecond, time.Millisecond}, 2*time.Second)()

	var attempts int32
	defer setDialWSForTest(func(ctx context.Context, url string, header http.Header) (*ws.Conn, error) {
		if n := atomic.AddInt32(&attempts, 1); n <= 2 {
			return nil, connectTimeout(int(n))
		}
		return ws.Dial(ctx, url, header)
	})()

	c := New(hub.server.URL, "testkey")
	conn, err := c.Dial(context.Background())
	if err != nil {
		t.Fatalf("two connect timeouts were not recovered: %v", err)
	}
	defer conn.Close()
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("dial attempts = %d, want 3 (two retries after two timeouts)", got)
	}
}

// A host that is really gone must be reported on the schedule, not after an
// unbounded number of attempts — and the error has to name every attempt, or
// the run's exit_reason cannot tell "the hub was down" from "one SYN was lost".
func TestDialGivesUpAfterTheBoundedSchedule(t *testing.T) {
	defer setDialScheduleForTest(
		[]time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond},
		2*time.Second)()

	var attempts int32
	defer setDialWSForTest(func(context.Context, string, http.Header) (*ws.Conn, error) {
		return nil, connectTimeout(int(atomic.AddInt32(&attempts, 1)))
	})()

	c := New("http://127.0.0.1:18799", "")
	_, err := c.Dial(context.Background())
	if err == nil {
		t.Fatal("a port that never answered was reported as connected")
	}
	if got := atomic.LoadInt32(&attempts); got != 5 {
		t.Fatalf("dial attempts = %d, want 5 (first connect + 4 retries)", got)
	}
	for _, want := range []string{"共 5 次尝试", "退避 1ms/1ms/1ms/1ms", "第 1 次", "第 5 次"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lost %q", err.Error(), want)
		}
	}
}

// The window is a ceiling on the whole phase, not a promise that every backoff
// token will be spent: a schedule longer than the window is clipped.
func TestDialStopsAtTheRetryWindow(t *testing.T) {
	defer setDialScheduleForTest([]time.Duration{time.Hour}, 50*time.Millisecond)()

	var attempts int32
	defer setDialWSForTest(func(context.Context, string, http.Header) (*ws.Conn, error) {
		return nil, connectTimeout(int(atomic.AddInt32(&attempts, 1)))
	})()

	start := time.Now()
	_, err := New("http://127.0.0.1:18799", "").Dial(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a dead port was reported as connected")
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("dial attempts = %d, want 2 (the 1h backoff was clipped to the 50ms window)", got)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Dial spent %s, want the window as the ceiling", elapsed)
	}
	if !strings.Contains(err.Error(), "整段上限 50ms") {
		t.Fatalf("error does not state the ceiling: %v", err)
	}
}

// A refusal is the host saying "no", not a lost packet. Retrying it only delays
// the real error, so it is returned on the first attempt.
func TestDialDoesNotRetryARefusedConnect(t *testing.T) {
	var attempts int32
	defer setDialWSForTest(func(context.Context, string, http.Header) (*ws.Conn, error) {
		atomic.AddInt32(&attempts, 1)
		return nil, errors.New("dial tcp 127.0.0.1:18799: connectex: 由于目标计算机积极拒绝，无法连接")
	})()

	_, err := New("http://127.0.0.1:18799", "").Dial(context.Background())
	if err == nil {
		t.Fatal("a refused connect was reported as connected")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("dial attempts = %d, want 1 (a refusal is final)", got)
	}
	if !strings.Contains(err.Error(), "未重试（不是连接超时）") {
		t.Fatalf("error does not say why it did not retry: %v", err)
	}
}

// Header text: a refusal that arrives *after* a timeout ends the loop at once.
// Waiting out the remaining backoff would only add seconds to a certainty.
func TestDialStopsWhenARefusalFollowsATimeout(t *testing.T) {
	defer setDialScheduleForTest([]time.Duration{time.Millisecond, time.Millisecond}, 2*time.Second)()

	var attempts int32
	defer setDialWSForTest(func(context.Context, string, http.Header) (*ws.Conn, error) {
		if n := atomic.AddInt32(&attempts, 1); n == 1 {
			return nil, connectTimeout(1)
		}
		return nil, errors.New("dial tcp 127.0.0.1:18799: connectex: 由于目标计算机积极拒绝，无法连接")
	})()

	_, err := New("http://127.0.0.1:18799", "").Dial(context.Background())
	if err == nil {
		t.Fatal("a refused connect was reported as connected")
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("dial attempts = %d, want 2 (a refusal after a timeout stops the loop)", got)
	}
	if !strings.Contains(err.Error(), "第 2 次不是超时，立即停止") {
		t.Fatalf("error does not record the early stop: %v", err)
	}
}

// A cancelled run must not wait out a backoff, even when the first attempt
// failed in a way that would otherwise be retried.
func TestDialDoesNotRetryAfterTheCallerCancels(t *testing.T) {
	defer setDialScheduleForTest([]time.Duration{time.Millisecond, time.Millisecond}, time.Second)()

	var attempts int32
	defer setDialWSForTest(func(context.Context, string, http.Header) (*ws.Conn, error) {
		atomic.AddInt32(&attempts, 1)
		return nil, connectTimeout(1)
	})()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New("http://127.0.0.1:18799", "").Dial(ctx)
	if err == nil {
		t.Fatal("a cancelled dial was reported as connected")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("dial attempts = %d, want 1 (a cancelled caller is not retried)", got)
	}
	if !strings.Contains(err.Error(), "未重试（context canceled）") {
		t.Fatalf("error does not record the cancellation: %v", err)
	}
}

// Cancelling during the backoff must end the phase at once: the next attempt
// would be sent on behalf of a run that no longer exists.
func TestDialStopsWhenCancelledDuringTheBackoff(t *testing.T) {
	defer setDialScheduleForTest([]time.Duration{300 * time.Millisecond}, time.Minute)()

	var attempts int32
	defer setDialWSForTest(func(context.Context, string, http.Header) (*ws.Conn, error) {
		atomic.AddInt32(&attempts, 1)
		return nil, connectTimeout(1)
	})()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := New("http://127.0.0.1:18799", "").Dial(ctx)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a cancelled dial was reported as connected")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("dial attempts = %d, want 1 (no attempt after cancellation)", got)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Dial spent %s after cancellation, want the backoff to be abandoned", elapsed)
	}
	if !strings.Contains(err.Error(), "调用方取消") {
		t.Fatalf("error does not record the cancellation: %v", err)
	}
}

func TestDialDoesNotRetryASuccessfulConnect(t *testing.T) {
	hub := startTestHub(t, func(*testHub, *ws.Conn, *pb.Envelope, *anypb.Any) {})
	defer hub.server.Close()

	var attempts int32
	defer setDialWSForTest(func(ctx context.Context, url string, header http.Header) (*ws.Conn, error) {
		atomic.AddInt32(&attempts, 1)
		return ws.Dial(ctx, url, header)
	})()

	c := New(hub.server.URL, "testkey")
	conn, err := c.Dial(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("dial attempts = %d, want 1 (no retry on success)", got)
	}
}

// The numbers are a decision, not an implementation detail: a later edit that
// widens the schedule or the ceiling changes how long a dead hub can stall a
// run, and this is the assertion that makes that edit deliberate. They are read
// from connectretry because the test harness retries the same failure and must
// use the same budget.
func TestDialRetryBudgetIsTheAgreedOne(t *testing.T) {
	want := []time.Duration{300 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}
	if !slices.Equal(connectretry.Delays, want) {
		t.Fatalf("shared retry schedule = %v, want %v", connectretry.Delays, want)
	}
	if connectretry.Window != 8*time.Second {
		t.Fatalf("shared retry window = %s, want 8s", connectretry.Window)
	}
	if !slices.Equal(connectRetryDelays, want) {
		t.Fatalf("client schedule = %v, want the shared %v", connectRetryDelays, want)
	}
	if connectRetryWindow != 8*time.Second {
		t.Fatalf("client retry window = %s, want 8s", connectRetryWindow)
	}
}

// connectTimeout builds the error a dropped SYN produces here: a net timeout,
// never a refusal.
func connectTimeout(attempt int) error {
	return fmt.Errorf("dial tcp 127.0.0.1:18799: connectex: 由于连接方在一段时间后没有正确答复或连接的主机没有反应: %w（第 %d 次）", os.ErrDeadlineExceeded, attempt)
}

// windowsSYNTimeout is the shape the Windows stack actually produces for a
// dropped SYN: a net.OpError whose errno is WSAETIMEDOUT.
func windowsSYNTimeout() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connectex", Err: syscall.Errno(10060)}}
}

// Windows reports a dropped SYN as WSAETIMEDOUT (10060), and Go's
// Errno.Timeout() does not accept that value, so the net.Error test alone
// classifies a real stall as "not a timeout" — measured 2026-11-22 in a full
// run, where the retry would have skipped the very failure it exists for. The
// errno has to be matched explicitly, and a refusal (10061) must stay final.
func TestConnectTimeoutRecognisesTheWindowsSYNTimeout(t *testing.T) {
	syn := windowsSYNTimeout()
	if isTransportTimeout(syn) {
		t.Fatal("test setup: this error is supposed to look like a non-timeout to the net.Error test")
	}
	if !isConnectTimeout(syn) {
		t.Fatalf("a WSAETIMEDOUT connect is not recognised as a timeout: %v", syn)
	}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connectex", Err: syscall.Errno(10061)}}
	if isConnectTimeout(refused) {
		t.Fatalf("a refused connect was classified as a timeout: %v", refused)
	}
}

// The same shape must be retried end to end, inside a bounded number of
// attempts. It does not need a live socket: the assertion is that a
// WSAETIMEDOUT connect is treated as retryable, which is exactly what a full
// run got wrong before the errno check existed.
func TestDialRetriesAWindowsSYNTimeout(t *testing.T) {
	defer setDialScheduleForTest([]time.Duration{time.Millisecond, time.Millisecond}, time.Second)()

	var attempts int32
	defer setDialWSForTest(func(context.Context, string, http.Header) (*ws.Conn, error) {
		atomic.AddInt32(&attempts, 1)
		return nil, windowsSYNTimeout()
	})()

	_, err := New("http://127.0.0.1:18799", "").Dial(context.Background())
	if err == nil {
		t.Fatal("a port that never answered was reported as connected")
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("dial attempts = %d, want 3 (first connect + two retries on a WSAETIMEDOUT)", got)
	}
	if !strings.Contains(err.Error(), "共 3 次尝试") {
		t.Fatalf("error does not record the attempts: %v", err)
	}
}

// setDialScheduleForTest shortens the schedule so the tests exercise the loop
// without spending the production backoff. It returns a restore.
func setDialScheduleForTest(delays []time.Duration, window time.Duration) func() {
	prevDelays, prevWindow := connectRetryDelays, connectRetryWindow
	connectRetryDelays, connectRetryWindow = delays, window
	return func() { connectRetryDelays, connectRetryWindow = prevDelays, prevWindow }
}

// setDialWSForTest swaps the package's handshake dial and returns a restore.
func setDialWSForTest(fn func(context.Context, string, http.Header) (*ws.Conn, error)) func() {
	prev := dialWS
	dialWS = fn
	return func() { dialWS = prev }
}
