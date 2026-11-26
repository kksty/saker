package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A missing board anchor must fail the round visibly. Before this, the insertion
// helper appended the section at the end of the prompt when no marker was found,
// so a renamed board heading silently buried the instruction section. Now the
// round fails with a machine-readable reason AND leaves a journal line, because
// run.status=failed alone is invisible to a tail and to the failure watch.
func TestMissingBoardAnchorFailsTheRoundVisibly(t *testing.T) {
	s, _, st := newCommanderServer(t)
	_, _, err := s.StartCommander(context.Background(), "## 本轮\nno board anchor here\n", false)
	if err == nil {
		t.Fatal("a round without the board anchor must fail instead of appending")
	}
	if !strings.Contains(err.Error(), "prompt_anchor_missing") {
		t.Fatalf("error is not machine-readable: %v", err)
	}
	var failed *model.Run
	runs := st.Runs()
	for i := range runs {
		if runs[i].Status == model.RunFailed && strings.Contains(runs[i].ExitReason, "prompt_anchor_missing") {
			failed = &runs[i]
			break
		}
	}
	if failed == nil {
		t.Fatalf("no failed run carries prompt_anchor_missing: %+v", runs)
	}
	raw, rerr := os.ReadFile(filepath.Join(s.DataDir, "runs", failed.ID+".jsonl"))
	if rerr != nil {
		t.Fatalf("read failed run journal: %v", rerr)
	}
	if !strings.Contains(string(raw), `"commander_failed"`) || !strings.Contains(string(raw), "prompt_anchor_missing") {
		t.Fatalf("failed round left no commander_failed journal line:\n%s", raw)
	}
}
