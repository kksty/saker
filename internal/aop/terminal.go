package aop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/kksty/saker/internal/aop/pb"
	"github.com/kksty/saker/internal/aop/pty"
	"github.com/kksty/saker/internal/connectretry"
	"github.com/kksty/saker/internal/ws"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// Terminal is one PTY stream on an AOP host.
//
// The harness carries PTY over the same application socket as sessions: an
// envelope whose Any payload is an aop.pty.ProtocolMessage. Open starts a
// shell, Input/Output carry bytes, Resize changes the window. We proxy those
// bytes to the console instead of embedding the host's own terminal (D11).
type Terminal struct {
	sock   *ws.Conn
	stream string
	// frame records that at least one PTY frame for this stream arrived. The
	// open retry below uses it as its hard stop: once the host has said
	// anything, handing a second shell to the same caller would duplicate work
	// the first stream already started.
	frame atomic.Bool
	// shared means this stream is attached to a singleton (main-repl) and must
	// detach on close; Close would kill the host's persistent console.
	shared bool

	out  chan []byte
	errs chan error
	done chan struct{}

	opened   chan struct{}
	openOnce sync.Once
	once     sync.Once
}

// OpenTerminal opens a shell on the agent behind an AOP host.
func OpenTerminal(ctx context.Context, baseURL, token, nodeID string, cols, rows int) (*Terminal, error) {
	return OpenTerminalKind(ctx, baseURL, token, nodeID, cols, rows, "shell", "", false)
}

// terminalOpenFailure carries why one open attempt failed, and whether the host
// had already sent a frame before it failed.
type terminalOpenFailure struct {
	err       error
	frameSeen bool
}

func (e *terminalOpenFailure) Error() string { return e.err.Error() }
func (e *terminalOpenFailure) Unwrap() error { return e.err }

// openAttemptWait is how long attempt N waits for the host's first frame. The
// waits are the connect-retry schedule itself (0.3/1/2/4s), and the last
// attempt gets whatever is left of connectretry.Window: a host slow to spawn a
// PTY gets more time on the retry instead of one arbitrary deadline, and the
// whole open stays inside the one budget this repository retries on.
func openAttemptWait(attempt int, deadline time.Time) time.Duration {
	wait := time.Until(deadline)
	if wait <= 0 {
		return 0
	}
	if attempt <= len(connectretry.Delays) {
		if d := connectretry.Delays[attempt-1]; d < wait {
			wait = d
		}
	}
	return wait
}

// retryableTerminalOpen reports whether a failed open attempt may be repeated.
// The boundary is deliberate: the caller has received nothing, the failure is a
// transport timeout, and no frame arrived. An answer from the host (an error
// frame, a closed stream, a refusal, a 401) is final, because the host has then
// made a statement about this stream.
func retryableTerminalOpen(err error) bool {
	var fail *terminalOpenFailure
	if errors.As(err, &fail) {
		return !fail.frameSeen && isConnectTimeout(fail.err)
	}
	// A dial error: no socket, so nothing was sent and nothing can be orphaned.
	return isConnectTimeout(err)
}

// OpenTerminalKind opens a PTY of a chosen kind. The host's interactive agent
// is kind=repl, name=main-repl and is a singleton: opening it again attaches to
// the live REPL instead of starting a second one. kind=shell keeps the old
// behaviour (a new shell per open).
//
// Opening is retried on the connect-retry budget for the same reason a connect
// is: this host stalls new connections for seconds, and a bridge that waited
// forever left the console with no answer at all (2026-11-22: a 3s terminal
// read timed out while the host was in a stall burst). Every attempt closes its
// own socket before the next one, so a stream the host did start ends with the
// connection that carried it.
func OpenTerminalKind(ctx context.Context, baseURL, token, nodeID string, cols, rows int, kind, name string, singleton bool) (*Terminal, error) {
	url := applicationWSURL(baseURL)
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	deadline := time.Now().Add(connectretry.Window)
	var causes []string
	var last error
	for attempt := 1; ; attempt++ {
		if attempt > 1 && !time.Now().Before(deadline) {
			break
		}
		wait := openAttemptWait(attempt, deadline)
		if attempt > 1 && wait <= 0 {
			break
		}
		t, err := openTerminalOnce(ctx, url, header, nodeID, cols, rows, kind, name, singleton, wait)
		if err == nil {
			if attempt > 1 {
				log.Printf("aop: 终端 %s 在第 %d 次尝试后打开（前 %d 次均无首帧）", url, attempt, attempt-1)
			}
			return t, nil
		}
		last = err
		causes = append(causes, err.Error())
		if attempt > len(connectretry.Delays) || ctx.Err() != nil || !retryableTerminalOpen(err) {
			break
		}
		log.Printf("aop: 终端 %s 第 %d 次打开失败（%v），预算内重试（第 %d/%d 次）",
			url, attempt, err, attempt, len(connectretry.Delays))
	}
	return nil, fmt.Errorf("终端：打开 %s 失败（共 %d 次尝试，预算 %s）：%s；最后 %w",
		url, len(causes), connectretry.Label(), strings.Join(causes, "；"), last)
}

// dialTermWS is the websocket handshake used when opening a terminal. A
// variable only so a test can inject a refusal or a timeout without depending
// on what this host's loopback stack does in that second; production never
// assigns it.
var dialTermWS = ws.Dial

// openTerminalOnce is one dial + open + wait for the host's first frame. On
// success the socket's read deadline is cleared: it bounded only the greeting.
func openTerminalOnce(ctx context.Context, url string, header http.Header, nodeID string, cols, rows int, kind, name string, singleton bool, wait time.Duration) (*Terminal, error) {
	sock, err := dialTermWS(ctx, url, header)
	if err != nil {
		return nil, fmt.Errorf("终端：连接 %s：%w", url, err)
	}
	t := &Terminal{
		sock:   sock,
		stream: "saker-term-" + newID(),
		shared: singleton,
		out:    make(chan []byte, 128),
		errs:   make(chan error, 1),
		done:   make(chan struct{}),
		opened: make(chan struct{}),
	}
	go t.read()
	if wait > 0 {
		_ = t.sock.SetReadDeadline(time.Now().Add(wait))
	}
	open := &pty.ProtocolMessage{Message: &pty.ProtocolMessage_Open{Open: &pty.Open{
		StreamId: t.stream, NodeId: nodeID, Kind: kind, Name: name,
		Cols: int32(cols), Rows: int32(rows), Singleton: singleton,
	}}}
	if err := t.send(open); err != nil {
		t.shutdown(err)
		return nil, &terminalOpenFailure{err: err}
	}
	select {
	case <-t.opened:
		_ = t.sock.SetReadDeadline(time.Time{})
		return t, nil
	case <-t.done:
		var cause error
		select {
		case cause = <-t.errs:
		default:
			cause = errors.New("终端：宿主关闭了连接")
		}
		return nil, &terminalOpenFailure{err: cause, frameSeen: t.frame.Load()}
	case <-ctx.Done():
		t.shutdown(ctx.Err())
		return nil, &terminalOpenFailure{err: ctx.Err(), frameSeen: t.frame.Load()}
	}
}

// Output streams terminal bytes. The channel is never closed; Done is.
func (t *Terminal) Output() <-chan []byte { return t.out }

// Errors delivers at most one terminal error.
func (t *Terminal) Errors() <-chan error { return t.errs }

// Done is closed when the stream ends, for either side.
func (t *Terminal) Done() <-chan struct{} { return t.done }

// Write sends keystrokes.
func (t *Terminal) Write(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	return t.send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Input{
		Input: &pty.Input{StreamId: t.stream, Data: p},
	}})
}

// Resize changes the PTY window.
func (t *Terminal) Resize(cols, rows int) error {
	return t.send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Resize{
		Resize: &pty.Resize{StreamId: t.stream, Cols: int32(cols), Rows: int32(rows)},
	}})
}

// Close ends the stream. A shell is killed; a singleton REPL is only detached,
// because the host owns it and a browser navigation is not "end the console".
// It is safe to call after an error.
func (t *Terminal) Close() error {
	if t.shared {
		_ = t.send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Detach{
			Detach: &pty.Detach{StreamId: t.stream},
		}})
	} else {
		_ = t.send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Close{
			Close: &pty.Close{StreamId: t.stream},
		}})
	}
	t.shutdown(nil)
	return nil
}

// Detach ends the stream without killing the PTY. The host keeps a singleton
// session (main-repl) alive, so a page refresh reattaches to the same
// conversation instead of starting a second REPL and replaying nothing.
func (t *Terminal) Detach() error {
	_ = t.send(&pty.ProtocolMessage{Message: &pty.ProtocolMessage_Detach{
		Detach: &pty.Detach{StreamId: t.stream},
	}})
	t.shutdown(nil)
	return nil
}

func (t *Terminal) send(msg *pty.ProtocolMessage) error {
	payload, err := anypb.New(msg)
	if err != nil {
		return err
	}
	raw, err := proto.Marshal(&pb.Envelope{Id: t.stream, Payload: payload})
	if err != nil {
		return err
	}
	return t.sock.WriteBinary(raw)
}

func (t *Terminal) shutdown(err error) {
	t.once.Do(func() {
		if err != nil {
			select {
			case t.errs <- err:
			default:
			}
		}
		close(t.done)
		_ = t.sock.Close()
	})
}

func (t *Terminal) read() {
	for {
		raw, readErr := t.sock.ReadMessage()
		if readErr != nil {
			t.shutdown(readErr)
			return
		}
		var env pb.Envelope
		if proto.Unmarshal(raw, &env) != nil || env.Payload == nil {
			continue
		}
		var msg pty.ProtocolMessage
		if env.Payload.UnmarshalTo(&msg) != nil {
			continue
		}
		if id := ptyStream(&msg); id != "" && id != t.stream {
			continue
		}
		// Any frame for this stream ends the open-retry window: the host has
		// already acted on the open, so repeating it would duplicate the work.
		t.frame.Store(true)
		switch v := msg.Message.(type) {
		case *pty.ProtocolMessage_Opened:
			t.openOnce.Do(func() { close(t.opened) })
		case *pty.ProtocolMessage_Attached:
			// The host answers an attach to an existing REPL with Attached, not
			// Opened. Without this case a REPL open waits forever even though
			// the stream is live.
			t.openOnce.Do(func() { close(t.opened) })
		case *pty.ProtocolMessage_Output:
			data := append([]byte(nil), v.Output.GetData()...)
			select {
			case t.out <- data:
			case <-t.done:
				return
			}
		case *pty.ProtocolMessage_Error:
			text := strings.TrimSpace(v.Error.GetMessage())
			if text == "" {
				text = "宿主返回了错误"
			}
			t.shutdown(fmt.Errorf("终端：%s", text))
			return
		case *pty.ProtocolMessage_Detached:
			t.shutdown(errors.New("终端：节点上没有在线的 agent"))
			return
		case *pty.ProtocolMessage_Closed:
			t.shutdown(io.EOF)
			return
		}
	}
}

// ptyStream reads the stream id out of any PTY message that carries one.
func ptyStream(msg *pty.ProtocolMessage) string {
	switch v := msg.Message.(type) {
	case *pty.ProtocolMessage_Open:
		return v.Open.GetStreamId()
	case *pty.ProtocolMessage_Opened:
		return v.Opened.GetStreamId()
	case *pty.ProtocolMessage_Attached:
		return v.Attached.GetStreamId()
	case *pty.ProtocolMessage_Input:
		return v.Input.GetStreamId()
	case *pty.ProtocolMessage_Output:
		return v.Output.GetStreamId()
	case *pty.ProtocolMessage_Resize:
		return v.Resize.GetStreamId()
	case *pty.ProtocolMessage_Attach:
		return v.Attach.GetStreamId()
	case *pty.ProtocolMessage_Detach:
		return v.Detach.GetStreamId()
	case *pty.ProtocolMessage_Close:
		return v.Close.GetStreamId()
	case *pty.ProtocolMessage_Closed:
		return v.Closed.GetStreamId()
	case *pty.ProtocolMessage_Kill:
		return v.Kill.GetStreamId()
	case *pty.ProtocolMessage_State:
		return v.State.GetStreamId()
	case *pty.ProtocolMessage_Detached:
		return v.Detached.GetStreamId()
	}
	return ""
}
