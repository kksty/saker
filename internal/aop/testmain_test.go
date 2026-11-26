package aop

import (
	"os"
	"testing"

	"github.com/kksty/saker/internal/testutil"
	"github.com/kksty/saker/internal/ws"
)

// TestMain gives the AOP client's real websocket connections the narrow
// dial-level retry allowed by docs/01-architecture.md §9: this host sometimes
// drops the SYN, so a healthy host would otherwise look unreachable.
func TestMain(m *testing.M) {
	restore := ws.SetNetDialForTests(testutil.RetryDial(nil))
	code := m.Run()
	restore()
	testutil.Summary()
	os.Exit(code)
}
