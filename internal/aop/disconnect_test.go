package aop

import (
	"errors"
	"io"
	"testing"

	"github.com/kksty/tianwu/internal/ws"
)

// A close code is the host's own statement about why the stream ended. Only a
// link-level death may restart the work; a deliberate close, and an
// unexplained go-away that claims nothing, must not be read as one.
func TestDisconnectClassReadsTheCloseCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"normal close", &ws.CloseError{Code: 1000, Text: "done"}, FailureIntentional},
		{"policy close", &ws.CloseError{Code: 1008, Text: "policy"}, FailureIntentional},
		{"going away, shutdown", &ws.CloseError{Code: 1001, Text: "server shutting down"}, FailureTransport},
		{"going away, restarting", &ws.CloseError{Code: 1001, Text: "host restarting"}, FailureTransport},
		{"going away, no reason", &ws.CloseError{Code: 1001}, FailureIntentional},
		{"protocol error", &ws.CloseError{Code: 1002}, FailureTransport},
		{"peer vanished", io.EOF, FailureUnknown},
		{"read error", errors.New("read tcp 127.0.0.1:1->127.0.0.1:2: connection reset by peer"), FailureTransport},
		{"no reason left", nil, FailureUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DisconnectClass(c.err); got != c.want {
				t.Fatalf("DisconnectClass(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}
