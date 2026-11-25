package api

import (
	"github.com/kksty/tianwu/internal/model"
	"testing"
	"time"
)

func TestConcurrentRetriesCannotResetAnAlreadyClaimedDirection(t *testing.T) {
	s, ex, in, _ := finishedServer(t, model.StatusFailed, model.RunFailed, true)
	errs := make(chan error, 8)
	for range 8 {
		go func() { _, _, err := s.retryIntentRun(in.ID, "parallel", true); errs <- err }()
	}
	wins := 0
	for range 8 {
		if <-errs == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("successful retries=%d", wins)
	}
	waitFor(t, time.Second, func() bool { return len(ex.requests()) == 1 })
	current, _ := s.Store.GetIntent(in.ID)
	if current.Hold || (current.Status != model.StatusRunning && current.Status != model.StatusClaimed) {
		t.Fatal("losing retry corrupted live claim")
	}
}
