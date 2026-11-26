package aop

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	pb "github.com/kksty/saker/internal/aop/pb"
	"github.com/kksty/saker/internal/aop/pty"
	"github.com/kksty/saker/internal/ws"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// Opening a terminal is the second entry point for the same host failure the
// connect retry covers: the socket comes up and then the host never sends the
// open acknowledgement, because its own upstream work is stalled. On 2026-11-22
// a console terminal read timed out at 3s while the host was in a stall burst
// and the bridge had written nothing at all.
//
// The retry boundary is narrower than the connect one, because an open can leave
// state on the host: it stops at the first frame the host sends, at any answer
// (error frame, closed stream, refusal), and at the caller's cancellation. What
// it may repeat is only the case where the caller has received nothing and the
// attempt died on a transport timeout.
func TestTerminalOpenRetriesASilentHost(t *testing.T) {
	var mu sync.Mutex
	opens := 0
	hub := startTestHub(t, func(_ *testHub, conn *ws.Conn, _ *pb.Envelope, payload *anypb.Any) {
		var msg pty.ProtocolMessage
		if payload.UnmarshalTo(&msg) != nil || msg.GetOpen() == nil {
			return
		}
		stream := ptyStream(&msg)
		mu.Lock()
		opens++
		n := opens
		mu.Unlock()
		if n == 1 {
			return // the first host says nothing at all
		}
		writePTY(t, conn, stream, &pty.ProtocolMessage{Message: &pty.ProtocolMessage_Opened{
			Opened: &pty.Opened{StreamId: stream}}})
	})
	defer hub.server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	term, err := OpenTerminal(ctx, hub.server.URL, "testkey", "node", 80, 24)
	if err != nil {
		t.Fatalf("a silent first host was not retried: %v", err)
	}
	defer term.Close()
	mu.Lock()
	got := opens
	mu.Unlock()
	if got != 2 {
		t.Fatalf("opens = %d, want 2 (the silent attempt, then a retry that answered)", got)
	}
}

// A frame ends the retry window even when the attempt then times out: the host
// has already started something for this stream, and a second open would
// duplicate it.
func TestTerminalOpenStopsOnceAFrameArrives(t *testing.T) {
	var mu sync.Mutex
	opens := 0
	hub := startTestHub(t, func(_ *testHub, conn *ws.Conn, _ *pb.Envelope, payload *anypb.Any) {
		var msg pty.ProtocolMessage
		if payload.UnmarshalTo(&msg) != nil || msg.GetOpen() == nil {
			return
		}
		stream := ptyStream(&msg)
		mu.Lock()
		opens++
		mu.Unlock()
		writePTY(t, conn, stream, &pty.ProtocolMessage{Message: &pty.ProtocolMessage_Output{
			Output: &pty.Output{StreamId: stream, Data: []byte("booting")}}})
		// …and never acknowledge the open.
	})
	defer hub.server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := OpenTerminal(ctx, hub.server.URL, "testkey", "node", 80, 24)
	if err == nil {
		t.Fatal("an open that never acknowledged was reported as opened")
	}
	mu.Lock()
	got := opens
	mu.Unlock()
	// The observable boundary is server-side: the host received exactly one
	// open, because the frame it sent ended the retry window. A retry before
	// any frame arrived (a stalled *dial* on this host) is allowed and does not
	// reach the host at all.
	if got != 1 {
		t.Fatalf("opens = %d, want 1 (a frame ended the retry window)", got)
	}
}

// A refusal is the host answering "no", so it is final: retrying would only
// delay the console's error line. The refusal is injected rather than produced
// by a closed port: under a stall this host also swallows the SYN of a closed
// port, and the test would then be measuring the stall, not the rule.
func TestTerminalOpenDoesNotRetryARefusedHost(t *testing.T) {
	var attempts int32
	defer setDialTermWSForTest(func(context.Context, string, http.Header) (*ws.Conn, error) {
		atomic.AddInt32(&attempts, 1)
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connectex", Err: syscall.Errno(10061)}}
	})()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	_, err := OpenTerminal(ctx, "http://127.0.0.1:1", "", "node", 80, 24)
	if err == nil {
		t.Fatal("a refused host was reported as opened")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("open attempts = %d, want 1 (a refusal is final)", got)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("a refusal took %s, want the first attempt (no retry)", elapsed)
	}
	if !strings.Contains(err.Error(), "共 1 次尝试") {
		t.Fatalf("error does not show a single attempt: %v", err)
	}
}

// setDialTermWSForTest swaps the terminal handshake dial and returns a restore.
func setDialTermWSForTest(fn func(context.Context, string, http.Header) (*ws.Conn, error)) func() {
	prev := dialTermWS
	dialTermWS = fn
	return func() { dialTermWS = prev }
}

// writePTY sends one PTY message from the host side of a test socket.
func writePTY(t *testing.T, conn *ws.Conn, stream string, msg *pty.ProtocolMessage) {
	t.Helper()
	payload, err := anypb.New(msg)
	if err != nil {
		t.Errorf("anypb: %v", err)
		return
	}
	raw, err := proto.Marshal(&pb.Envelope{Id: stream, Payload: payload})
	if err != nil {
		t.Errorf("marshal envelope: %v", err)
		return
	}
	if err := conn.WriteBinary(raw); err != nil {
		t.Errorf("write frame: %v", err)
	}
}
