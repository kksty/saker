package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

func TestSaveHandoffVersionCASAndProfile(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "父", Profile: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusPending }); err != nil {
		t.Fatal(err)
	}
	held, err := s.BeginHandoff(in.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.SaveHandoff(in.ID, 0, model.Handoff{ToProfile: "new", Note: "交接"}, held.HandoffOperation)
	if err != nil {
		t.Fatal(err)
	}
	if h.HandoffVersion != 1 || h.Handoff == nil || h.Handoff.Version != 1 || h.Handoff.ID == "" {
		t.Fatalf("saved = %+v", h)
	}
	if h.Profile != "new" || h.Status != model.StatusPending {
		t.Fatalf("intent after handoff = %+v", h)
	}
	// An old turn cannot overwrite the new handoff.
	if _, err := s.SaveHandoff(in.ID, 0, model.Handoff{ToProfile: "third"}); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("stale handoff err = %v", err)
	}
	if err := s.ReleaseHandoffHold(in.ID, "下一次交接", held.HandoffOperation); err != nil {
		t.Fatal(err)
	}
	held, err = s.BeginHandoff(in.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.SaveHandoff(in.ID, 1, model.Handoff{ToProfile: "third"}, held.HandoffOperation)
	if err != nil || next.HandoffVersion != 2 {
		t.Fatalf("second handoff = %+v %v", next, err)
	}
}

func TestHandoffFreezesFactReferences(t *testing.T) {
	s := noteStore(t)
	in, err := s.CreateIntent(model.Intent{Title: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFact(model.Fact{Kind: "note", Title: "first claim", IntentID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	held, err := s.BeginHandoff(in.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveHandoff(in.ID, 0, model.Handoff{Facts: []string{f.ID}}, held.HandoffOperation)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Handoff.FactVersions[f.ID] != 1 || saved.Handoff.FactClaimVersions[f.ID] != f.ClaimVersion {
		t.Fatalf("no frozen versions: %+v", saved.Handoff)
	}
	saved.Handoff.FactVersions[f.ID] = 99
	if _, err := s.UpdateFact(f.ID, func(f *model.Fact) { f.Doc = "changed claim" }); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.GetIntent(in.ID)
	if stored.Handoff.FactVersions[f.ID] != 1 || stored.Handoff.FactClaimVersions[f.ID] != f.ClaimVersion {
		t.Fatal("historical handoff reference changed with current claim or caller mutation")
	}
}

// BeginHandoff is the abort point for a stale/duplicate request: it must fail
// without changing a single field, because the caller cancels the live run only
// after this succeeds.
func TestBeginHandoffCASLeavesStateUntouchedOnConflict(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "父"})
	if err != nil {
		t.Fatal(err)
	}
	// A real dispatch archives the prompt it sent; the gate reads that archive.
	live, err := s.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	archiveRun(t, s, live.ID)
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status = model.StatusRunning
		i.RunID = live.ID
		i.HandoffVersion = 1
		i.Hold = false
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetIntent(in.ID)

	if _, err := s.BeginHandoff(in.ID, 0); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("stale begin = %v, want ErrHandoffConflict", err)
	}
	after, _ := s.GetIntent(in.ID)
	if after.HandoffVersion != before.HandoffVersion || after.Hold || after.Status != model.StatusRunning ||
		after.RunID != before.RunID {
		t.Fatalf("stale begin changed the intent: before=%+v after=%+v", before, after)
	}

	// The matching version wins and takes the hold without touching the run.
	cur, err := s.BeginHandoff(in.ID, 1)
	if err != nil || !cur.Hold || cur.RunID != live.ID {
		t.Fatalf("begin = %+v %v", cur, err)
	}
}

// archiveRun records what a real dispatch leaves behind. Handoff fixtures need
// it because the gate refuses to replace a run that never archived its prompt.
func archiveRun(t *testing.T, s *Store, runID string) {
	t.Helper()
	if _, err := s.UpdateRun(runID, func(r *model.Run) { r.BriefPath = ".saker/briefs/" + r.ID + ".md" }); err != nil {
		t.Fatal(err)
	}
}

// Hold survives SaveHandoff and is released by the successful claim of the
// replacement run (or explicitly when the handoff aborts).
func TestHandoffHoldSpansSnapshotUntilClaim(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	in, _ := s.CreateIntent(model.Intent{Title: "父"})
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusPending }); err != nil {
		t.Fatal(err)
	}
	held, err := s.BeginHandoff(in.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveHandoff(in.ID, 0, model.Handoff{ToProfile: "new"}, held.HandoffOperation)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Hold {
		t.Fatal("SaveHandoff dropped the handoff hold")
	}
	if _, ok, err := s.ClaimIntent(in.ID, "local"); !errors.Is(err, ErrHandoffConflict) || ok {
		t.Fatalf("unowned claim = %v %v", ok, err)
	}
	if _, ok, err := s.ClaimIntent(in.ID, "local", held.HandoffOperation); err != nil || !ok {
		t.Fatalf("claim = ok:%v err:%v", ok, err)
	}
	claimed, _ := s.GetIntent(in.ID)
	if claimed.Hold {
		t.Fatal("claim did not clear the handoff hold")
	}
}

// Aborting before the snapshot releases the hold so the direction can be
// scheduled like any other pending unit.
func TestReleaseHandoffHold(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	in, _ := s.CreateIntent(model.Intent{Title: "父"})
	held, err := s.BeginHandoff(in.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseHandoffHold(in.ID, "测试中止", held.HandoffOperation); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.GetIntent(in.ID)
	if cur.Hold {
		t.Fatal("hold survived ReleaseHandoffHold")
	}
}

// The refusal has to be recoverable, so the gate decides it before anything
// moves: the old run keeps the direction and no snapshot is written. Refusing
// at dispatch instead would leave the direction failed with that snapshot
// still on it, and every retry would hit the same wall.
func TestBeginHandoffRefusesUnarchivedSourceWithoutTouchingTheDirection(t *testing.T) {
	s := open(t)
	in, err := s.CreateIntent(model.Intent{Title: "接手的意图"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunFailed }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) {
		i.Status, i.RunID = model.StatusPending, run.ID
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetIntent(in.ID)

	_, err = s.BeginHandoff(in.ID, 0)
	if !errors.Is(err, ErrHandoffUnarchived) {
		t.Fatalf("begin = %v, want ErrHandoffUnarchived", err)
	}
	if !strings.Contains(err.Error(), run.ID) {
		t.Fatalf("the refusal does not name the run it refused: %v", err)
	}
	after, _ := s.GetIntent(in.ID)
	if after.HandoffOperation != "" || after.Hold || after.Handoff != nil ||
		after.Status != before.Status || after.RunID != before.RunID ||
		after.HandoffVersion != before.HandoffVersion {
		t.Fatalf("the refusal changed the direction: before=%+v after=%+v", before, after)
	}

	// The same call is accepted once the replaced run has archived its prompt.
	archiveRun(t, s, run.ID)
	if held, err := s.BeginHandoff(in.ID, 0); err != nil || !held.Hold {
		t.Fatalf("handoff from an archived run = %+v %v", held, err)
	}
}

// A direction that never ran has no prompt anybody could have paraphrased, so
// demanding an archive there would remove handoff from exactly the case where
// it is cheapest.
func TestBeginHandoffOfANeverRunDirectionNeedsNoArchive(t *testing.T) {
	s := open(t)
	in, err := s.CreateIntent(model.Intent{Title: "新建意图"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIntent(in.ID, func(i *model.Intent) { i.Status = model.StatusPending }); err != nil {
		t.Fatal(err)
	}
	held, err := s.BeginHandoff(in.ID, 0)
	if err != nil {
		t.Fatalf("handoff of a never-run direction = %v", err)
	}
	if !held.Hold || held.HandoffOperation == "" {
		t.Fatalf("begin = %+v, want the hold taken", held)
	}
}
