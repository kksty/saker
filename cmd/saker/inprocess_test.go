package main

import (
	"net/http"
	"testing"

	"github.com/kksty/saker/internal/testutil"
)

// newInProcessHost registers handler under a unique host name and returns the
// base URL for it. The request is dispatched straight into the handler by the
// transport installed in TestMain, so a test that only asserts an HTTP contract
// does not depend on the host's loopback stack, which is documented as
// unreliable here (docs/01-architecture.md §9: SYNs are occasionally dropped
// and connect() reports a ~1s timeout even though the listener is healthy).
//
// Tests that need a refused port, a WebSocket upgrade or a streaming response
// keep their own real listener.
func newInProcessHost(t *testing.T, handler http.Handler) string {
	t.Helper()
	return testutil.NewServer(t, handler).URL
}
