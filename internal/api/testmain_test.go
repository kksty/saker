package api

import (
	"os"
	"testing"

	"github.com/kksty/saker/internal/testutil"
	"github.com/kksty/saker/internal/ws"
)

// TestMain installs the in-process host transport, the narrow dial-level retry
// for the sockets a test cannot avoid, and reports how often a retry was needed.
// See docs/01-architecture.md §9 for why the retry exists and its limits.
func TestMain(m *testing.M) {
	testutil.Install()
	restore := ws.SetNetDialForTests(testutil.RetryDial(nil))
	code := m.Run()
	restore()
	testutil.Summary()
	os.Exit(code)
}
