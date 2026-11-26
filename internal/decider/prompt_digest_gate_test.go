package decider

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/board"
)

// The bounded-list label ("these rows are a top-N, the totals are the ledger")
// is orientation for a session that may not have it. Re-sending the same
// paragraph every round is a fixed cost with no new information, so it rides
// the rounds where memory was actually lost and drops on an ordinary resume.
func TestBoundedListDigestRidesOnlyRoundsThatMayHaveLostMemory(t *testing.T) {
	cases := []struct {
		name string
		rc   RoundContext
		want bool
	}{
		{"fresh session", RoundContext{}, true},
		{"planned rotation", RoundContext{Resume: true, Rotated: true}, true},
		{"compaction", RoundContext{Resume: true, Compacted: true}, true},
		{"restart", RoundContext{Resume: true, Restarted: true}, true},
		{"ordinary resume", RoundContext{Resume: true}, false},
	}
	for _, c := range cases {
		p := CommanderRoundPrompt(board.View{}, "", "", ContextStatus{}, c.rc)
		got := strings.Contains(p, `"prompt_digest"`)
		if got != c.want {
			t.Fatalf("%s: digest present=%v, want %v", c.name, got, c.want)
		}
	}
	// Dropping the label must not hide the ledger-wide counts: they are what
	// still tells the model that a list may be truncated.
	p := CommanderRoundPrompt(board.View{}, "", "", ContextStatus{}, RoundContext{Resume: true})
	if !strings.Contains(p, `## board`) || !strings.Contains(p, `"total"`) {
		t.Fatalf("ordinary round lost the board totals: %s", p)
	}
}
