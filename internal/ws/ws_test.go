package ws

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kksty/saker/internal/testutil"
)

func TestEchoRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage([]byte("echo:" + string(msg))); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialTestServer(t, ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, want := range []string{"hello", strings.Repeat("x", 70_000)} {
		if err := c.WriteMessage([]byte(want)); err != nil {
			t.Fatal(err)
		}
		got, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "echo:"+want {
			t.Fatalf("echo mismatch: %d bytes back", len(got))
		}
	}
}

func TestUpgradeRejectsPlainGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Upgrade(w, r); err == nil {
			t.Fatal("plain GET must not upgrade")
		}
	}))
	defer srv.Close()
	resp, err := testutil.RetryConnect(t, "plain GET", func() (*http.Response, error) { return http.Get(srv.URL) })
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestPingIsAnsweredTransparently(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		// Server sends a ping, then a text message: the client must skip the
		// ping without surfacing it.
		_ = c.writeFrame(opPing, []byte("are you there"))
		_ = c.WriteMessage([]byte("after-ping"))
		_, _ = c.ReadMessage() // wait for the pong (or the close)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialTestServer(t, ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	msg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(msg) != "after-ping" {
		t.Fatalf("got %q", msg)
	}
}

func TestServerRejectsMaskedClientFrame(t *testing.T) {
	// A server that receives a masked frame is being spoken to by a client that
	// got its role backwards; the connection must fail loudly, not decode junk.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		if _, err := c.ReadMessage(); err == nil {
			t.Fatal("masked frame from a client was accepted")
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialTestServer(t, ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Hand-write an unmasked frame: that is exactly the server's rule being
	// broken from the client side.
	c.mu.Lock()
	_, _ = c.conn.Write([]byte{0x81, 0x02, 'h', 'i'})
	c.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
}

// A peer that stops reading must not block Write forever (and, through the
// write mutex, must not block Close either).
func TestWriteTimesOutWhenPeerDoesNotRead(t *testing.T) {
	old := writeTimeout
	writeTimeout = 80 * time.Millisecond
	defer func() { writeTimeout = old }()

	a, b := net.Pipe()
	defer b.Close()
	c := &Conn{conn: a}

	start := time.Now()
	err := c.WriteBinary(make([]byte, 1<<20))
	if err == nil {
		t.Fatal("a write to a stalled peer must fail, not block")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("write blocked for %s", d)
	}
}

func TestCloseDoesNotWaitForAStalledWrite(t *testing.T) {
	old := writeTimeout
	writeTimeout = 150 * time.Millisecond
	defer func() { writeTimeout = old }()

	a, b := net.Pipe()
	defer b.Close()
	c := &Conn{conn: a}
	go func() { _ = c.WriteBinary(make([]byte, 1<<20)) }()
	time.Sleep(20 * time.Millisecond) // let the write take the mutex

	start := time.Now()
	_ = c.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Close waited %s for a stalled write", d)
	}
}

// A host that accepts the TCP connection and never answers the upgrade must not
// hold the caller past the handshake deadline (DialContext only covers connect).
func TestDialHandshakeIsBounded(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		time.Sleep(2 * time.Second) // accept, never respond
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Dial(ctx, "ws://"+ln.Addr().String()+"/x", nil); err == nil {
		t.Fatal("handshake against a mute host must fail")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("handshake blocked for %s", d)
	}
}

// closePayload builds the body of a close frame: a 2-byte code then UTF-8 text.
func closePayload(code int, text string) []byte {
	p := make([]byte, 2, 2+len(text))
	binary.BigEndian.PutUint16(p, uint16(code))
	return append(p, text...)
}

// A peer that ends the stream on purpose says why. That fact has to survive the
// read path: flattening a close frame into io.EOF made an intentional shutdown
// and a broken link indistinguishable, which is the whole point of keeping the
// code and text.
func TestCloseFrameKeepsThePeersReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.writeFrame(opClose, closePayload(1001, "host closing"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialTestServer(t, ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.ReadMessage()
	var closed *CloseError
	if !errors.As(err, &closed) {
		t.Fatalf("a close frame must surface as *CloseError, got %v", err)
	}
	if closed.Code != 1001 || closed.Text != "host closing" {
		t.Fatalf("close = %d %q", closed.Code, closed.Text)
	}
	// Callers that only care that the stream ended keep working.
	if !errors.Is(err, io.EOF) {
		t.Fatal("a close frame stopped being end-of-stream for errors.Is(err, io.EOF)")
	}
}

// An empty close payload carries no code; RFC 6455 names that 1005, not 0.
func TestEmptyCloseFrameIsCode1005(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.writeFrame(opClose, nil)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialTestServer(t, ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.ReadMessage()
	var closed *CloseError
	if !errors.As(err, &closed) {
		t.Fatalf("an empty close frame must surface as *CloseError, got %v", err)
	}
	if closed.Code != 1005 || closed.Text != "" {
		t.Fatalf("close = %d %q, want 1005 with no text", closed.Code, closed.Text)
	}
}

// A close frame ends that connection, not the endpoint. Every AOP attempt dials
// fresh, so a close path that poisoned the next Dial would break the retry.
func TestDialAfterPeerCloseStillConnects(t *testing.T) {
	var mu sync.Mutex
	conns := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		mu.Lock()
		conns++
		first := conns == 1
		mu.Unlock()
		if first {
			_ = c.writeFrame(opClose, closePayload(1001, "host closing"))
			return
		}
		msg, err := c.ReadMessage()
		if err != nil {
			return
		}
		_ = c.WriteMessage(append([]byte("echo:"), msg...))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	first, err := testutil.RetryConnect(t, "ws dial after a peer close (first)", func() (*Conn, error) {
		return Dial(ctx, url, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.ReadMessage(); err == nil {
		t.Fatal("the first connection did not report the peer's close")
	}
	_ = first.Close()

	second, err := testutil.RetryConnect(t, "ws redial after a peer close", func() (*Conn, error) {
		return Dial(ctx, url, nil)
	})
	if err != nil {
		t.Fatalf("redial after a peer close failed: %v", err)
	}
	defer second.Close()
	if err := second.WriteMessage([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	got, err := second.ReadMessage()
	if err != nil || string(got) != "echo:hi" {
		t.Fatalf("second connection = %q, %v", got, err)
	}
}

// dialTestServer is how the ws tests open a real socket. This host drops SYNs
// for seconds at a time (measured 2-12s, idle machine included), and "the test
// server is up but the connect timed out" is not a fact about the code under
// test. Only the connect phase is retried, with the shared testutil budget: a
// handshake answer, a close frame or a protocol error still fails on the first
// attempt — TestDialHandshakeIsBounded deliberately keeps its bare Dial.
func dialTestServer(t *testing.T, ctx context.Context, srv *httptest.Server) (*Conn, error) {
	t.Helper()
	return testutil.RetryConnect(t, "ws dial", func() (*Conn, error) {
		return Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	})
}

// The helper exists so a stalled first connect does not fail the test: install a
// dialer that drops the first SYN and check the handshake still completes, on a
// second attempt. Without the retry this test hangs on the synthetic timeout.
func TestDialTestServerRetriesAStalledFirstConnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage([]byte("hello"))
	}))
	defer srv.Close()

	var attempts int32
	restore := SetNetDialForTests(func(ctx context.Context, network, addr string) (net.Conn, error) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			return nil, &net.OpError{Op: "dial", Net: network, Err: os.ErrDeadlineExceeded}
		}
		return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, addr)
	})
	defer restore()

	ctx, cancel := context.WithTimeout(context.Background(), testutil.FixtureWindow)
	defer cancel()
	c, err := dialTestServer(t, ctx, srv)
	if err != nil {
		t.Fatalf("dial after one stalled connect: %v", err)
	}
	defer c.Close()
	got, err := c.ReadMessage()
	if err != nil || string(got) != "hello" {
		t.Fatalf("greeting = %q, %v", got, err)
	}
	if n := atomic.LoadInt32(&attempts); n < 2 {
		t.Fatalf("the stalled connect was not retried: %d attempt(s)", n)
	}
}
