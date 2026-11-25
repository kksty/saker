package store

import (
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

func newCandidateGroup(t *testing.T, s *Store, group string, n int) []model.Intent {
	t.Helper()
	out := make([]model.Intent, 0, n)
	for i := 0; i < n; i++ {
		in, err := s.CreateIntent(model.Intent{Title: "候选", CandidateGroup: group})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, in)
	}
	return out
}

// A fresh group starts all-pending and none has ever been dispatched, so the
// first candidate must be free to go. Counting pending as occupied would make
// the members block each other and the group could never start.
func TestCandidateGroupAllPendingCanDispatchOne(t *testing.T) {
	s := open(t)
	group := newCandidateGroup(t, s, "g_alt", 3)
	if owner, ok := s.RunningInCandidateGroup("g_alt", group[0].ID); ok {
		t.Fatalf("a never-dispatched candidate occupies the group: %s", owner.ID)
	}
}

// waiting is still in flight: its run ended but its subtree runs and it will be
// woken, so a sibling must not start alongside it.
func TestCandidateGroupWaitingOccupies(t *testing.T) {
	s := open(t)
	group := newCandidateGroup(t, s, "g_alt", 2)
	a, b := group[0], group[1]
	if _, _, err := s.StartIntentRun(a.ID, "local", "", model.Run{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(a.ID, func(i *model.Intent) { i.Status = model.StatusWaiting }); err != nil {
		t.Fatal(err)
	}
	owner, ok := s.RunningInCandidateGroup("g_alt", b.ID)
	if !ok || owner.ID != a.ID {
		t.Fatalf("a waiting candidate did not occupy the group: owner=%+v ok=%v", owner, ok)
	}
}

// A settled candidate frees the group again: done/failed/parked are starting
// points for a later retry, not an in-flight alternative.
func TestCandidateGroupSettledFreesTheGroup(t *testing.T) {
	s := open(t)
	group := newCandidateGroup(t, s, "g_alt", 2)
	a, b := group[0], group[1]
	if _, _, err := s.StartIntentRun(a.ID, "local", "", model.Run{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(a.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	if owner, ok := s.RunningInCandidateGroup("g_alt", b.ID); ok {
		t.Fatalf("a settled candidate still occupies the group: %s", owner.ID)
	}
}
