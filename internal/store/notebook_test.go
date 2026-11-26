package store

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kksty/saker/internal/model"
)

func noteStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func noteRun(t *testing.T, s *Store) model.Run {
	t.Helper()
	r, err := s.AddRun(model.Run{Purpose: "commander", CommanderRound: 7})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBlankNoteBodyIsNotAHandoffReceipt(t *testing.T) {
	// The identity, run and hash all line up in this case; only the body is
	// empty. Counting that as a handoff closes the pressure cycle on top of
	// empty memory and can arm a rotation to a session that has nothing to
	// read.
	for _, body := range []string{"", "   \n\t "} {
		s := noteStore(t)
		run := noteRun(t, s)
		if _, err := s.SaveNotebook(0, body, "commander", run.ID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.ValidNoteHandoff(run.ID, 0); ok {
			t.Fatalf("blank body %q proved a handoff", body)
		}
	}
	s := noteStore(t)
	run := noteRun(t, s)
	note, err := s.SaveNotebook(0, "结论与线索", "commander", run.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidNoteHandoff(run.ID, note.Version-1); !ok {
		t.Fatal("a written body lost its receipt")
	}
}

func TestNotebookCASReceiptsAndRecovery(t *testing.T) {
	s := noteStore(t)
	run := noteRun(t, s)
	f, err := s.AddFact(model.Fact{Kind: "note", Title: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := s.FactVersionsAt(s.Watermark())
	if err != nil {
		t.Fatal(err)
	}
	beforeRev, beforeHash := s.Revision(), s.ContentHash()
	note, err := s.SaveNotebook(0, "判断，引用 "+f.ID, "commander", run.ID, refs)
	if err != nil {
		t.Fatal(err)
	}
	if note.Version != 1 || note.Round != 7 || note.RunID != run.ID || note.SHA256 != NoteHash(note.State) || note.FactClaimVersions[f.ID] != f.ClaimVersion {
		t.Fatalf("bad receipt: %+v", note)
	}
	if s.Revision() != beforeRev || s.ContentHash() != beforeHash {
		t.Fatal("private note became shared news")
	}
	if _, err := s.SaveNotebook(0, "stale", "operator", "", nil); !errors.Is(err, ErrNoteConflict) {
		t.Fatalf("stale overwrite: %v", err)
	}
	if _, ok := s.ValidNoteHandoff(run.ID, 0); ok {
		t.Fatal("unfinished run proved a handoff")
	}
	if _, err := s.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidNoteHandoff(run.ID, 0); !ok {
		t.Fatal("successful writer lost its receipt")
	}
	if _, ok := s.ValidNoteHandoff("other", 0); ok {
		t.Fatal("another round borrowed receipt")
	}
	if _, ok := s.ValidNoteHandoff(run.ID, 1); ok {
		t.Fatal("preexisting version satisfied handoff")
	}
	if _, err := s.SaveNotebook(1, "operator correction", "operator", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidNoteHandoff(run.ID, 0); ok {
		t.Fatal("operator overwrite satisfied commander's handoff")
	}
	if _, err := s.SaveNotebook(2, "late", "commander", run.ID, nil); !errors.Is(err, ErrNoteConflict) {
		t.Fatalf("settled writer accepted: %v", err)
	}
	refs[f.ID] = 999
	note.FactVersions[f.ID] = 999
	old, ok := s.NotebookVersion(1)
	if !ok || old.FactVersions[f.ID] != 1 {
		t.Fatalf("history shares mutable references: %+v", old)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	old, ok = reopened.NotebookVersion(1)
	if !ok || old.SHA256 != NoteHash(old.State) || old.RunID != run.ID {
		t.Fatal("note history lost across restart")
	}
	savedRun, _ := reopened.GetRun(run.ID)
	if len(savedRun.NoteWrites) != 1 || savedRun.NoteWrites[0] != old.NoteRef {
		t.Fatal("writer receipt lost across restart")
	}
	view, v := reopened.ReadView()
	if len(view.Notebooks) != 0 {
		t.Fatal("private note leaked into shared snapshot")
	}
	if view, err = reopened.ReadAt(v); err != nil || len(view.Notebooks) != 0 {
		t.Fatal("private note leaked into frozen snapshot")
	}
	changes, _, err := reopened.ChangeWindow(v, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Kind == "notebook" {
			t.Fatal("private note leaked into changes")
		}
	}
}

func TestConcurrentNotebookReplacementHasOneWinner(t *testing.T) {
	s := noteStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, text := range []string{"a", "b"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			_, err := s.SaveNotebook(0, text, "operator", "", nil)
			errs <- err
		}(text)
	}
	wg.Wait()
	close(errs)
	wins, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrNoteConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 || len(s.History("notebook", "commander")) != 1 {
		t.Fatal("raced writes both succeeded or left extra versions")
	}
}

func TestNotebookRejectsStaleFactAndRollsBackFailedCommit(t *testing.T) {
	s := noteStore(t)
	run := noteRun(t, s)
	f, err := s.AddFact(model.Fact{Kind: "note", Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	refs, _ := s.FactVersionsAt(s.Watermark())
	if _, err := s.UpdateFact(f.ID, func(f *model.Fact) { f.Title = "second" }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveNotebook(0, "stale source", "commander", run.ID, refs); !errors.Is(err, ErrNoteConflict) {
		t.Fatalf("stale fact accepted: %v", err)
	}
	if _, err := s.sqlite.db.Exec(`CREATE TRIGGER reject_note BEFORE INSERT ON objects WHEN NEW.kind='notebook' BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveNotebook(0, "failed commit", "commander", run.ID, nil); err == nil {
		t.Fatal("failed transaction reported success")
	}
	if _, ok := s.Notebook(); ok {
		t.Fatal("failed transaction left a note")
	}
	run, _ = s.GetRun(run.ID)
	if len(run.NoteWrites) != 0 || len(s.History("notebook", "commander")) != 0 {
		t.Fatal("failed transaction left a receipt")
	}
}

// The writer is provenance, not a label the caller picks: only a live commander
// run may carry the commander writer, and only the bare operator writer may
// write without a run. A forged writer never reaches the ledger.
func TestNotebookWriterAndRunBindingAreEnforced(t *testing.T) {
	s := noteStore(t)
	execRun, err := s.AddRun(model.Run{Purpose: "executor", Status: model.RunRunning})
	if err != nil {
		t.Fatal(err)
	}
	commanderRun := noteRun(t, s)
	if _, err := s.SaveNotebook(0, "forged", "commander", execRun.ID, nil); !errors.Is(err, ErrNoteConflict) {
		t.Fatalf("executor run accepted as commander writer: %v", err)
	}
	if _, err := s.SaveNotebook(0, "forged", "operator", commanderRun.ID, nil); err == nil {
		t.Fatal("operator writer accepted a run binding")
	}
	if _, err := s.SaveNotebook(0, "forged", "commander", "r_missing", nil); !errors.Is(err, ErrNoteConflict) {
		t.Fatalf("unknown run accepted as commander writer: %v", err)
	}
	if _, err := s.SaveNotebook(0, "forged", "intruder", "", nil); err == nil {
		t.Fatal("unknown writer accepted")
	}
	if _, ok := s.Notebook(); ok {
		t.Fatal("a rejected writer left a note")
	}
	if _, err := s.SaveNotebook(0, "commander judgement", "commander", commanderRun.ID, nil); err != nil {
		t.Fatalf("live commander writer rejected: %v", err)
	}
}

// An operator rewrite during a commander round is audited on that running round
// and explicitly labelled as not a handoff receipt. Finished rounds and other
// purposes are not touched.
func TestOperatorRewriteDuringCommanderRoundIsAudited(t *testing.T) {
	s := noteStore(t)
	live := noteRun(t, s)
	done := noteRun(t, s)
	if _, err := s.UpdateRun(done.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	execRun, err := s.AddRun(model.Run{Purpose: "executor", Status: model.RunRunning})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveNotebook(0, "operator note", "operator", "", nil); err != nil {
		t.Fatal(err)
	}
	liveAfter, _ := s.GetRun(live.ID)
	if len(liveAfter.Infos) != 1 || !strings.Contains(liveAfter.Infos[0], "操作员凭据改写笔记 v1") ||
		!strings.Contains(liveAfter.Infos[0], "不作为指挥官交接回执") {
		t.Fatalf("live round audit = %+v", liveAfter.Infos)
	}
	doneAfter, _ := s.GetRun(done.ID)
	if len(doneAfter.Infos) != 0 {
		t.Fatalf("finished round was audited: %+v", doneAfter.Infos)
	}
	execAfter, _ := s.GetRun(execRun.ID)
	if len(execAfter.Infos) != 0 {
		t.Fatalf("executor run was audited: %+v", execAfter.Infos)
	}
	// The operator write is not a commander receipt, and the audit does not
	// turn it into one.
	if _, ok := s.ValidNoteHandoff(live.ID, 0); ok {
		t.Fatal("operator write satisfied the live commander round's handoff")
	}
}

// A newer round's write supersedes the older receipt: the previous round's
// receipt still exists on its run row, but it can not prove the current handoff.
func TestNewerRoundSupersedesOlderReceipt(t *testing.T) {
	s := noteStore(t)
	first := noteRun(t, s)
	if _, err := s.SaveNotebook(0, "first", "commander", first.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRun(first.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidNoteHandoff(first.ID, 0); !ok {
		t.Fatal("first round's own receipt was not valid")
	}
	second := noteRun(t, s)
	if _, err := s.SaveNotebook(1, "second", "commander", second.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidNoteHandoff(first.ID, 0); ok {
		t.Fatal("old round's receipt proved a handoff after a newer write")
	}
	if _, ok := s.ValidNoteHandoff(second.ID, 1); ok {
		t.Fatal("unfinished round proved a handoff")
	}
	if _, err := s.UpdateRun(second.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ValidNoteHandoff(second.ID, 1); !ok {
		t.Fatal("current round's own receipt was not valid after it finished")
	}
}
