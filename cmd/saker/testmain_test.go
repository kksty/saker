package main

import (
	"os"
	"testing"

	"github.com/kksty/saker/internal/testutil"
)

// TestMain installs the in-process host transport before any test builds an
// HTTP client. The product clients under test leave Transport nil, so one swap
// lets a client test answer from a handler instead of a loopback listener,
// whose host stack is documented as unreliable (docs/01-architecture.md §9).
// Requests to hosts the tests did not register are untouched.
func TestMain(m *testing.M) {
	testutil.Install()
	code := m.Run()
	testutil.Summary()
	os.Exit(code)
}
