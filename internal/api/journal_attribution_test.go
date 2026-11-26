package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A run whose dispatch dies before the harness says anything used to leave a
// zero-byte journal. The TSecBench 2026-11-22 review package had three of them
// (r_dhbjxdrdie, r_h43cm6mjqi, r_wohadshbzr), and the only evidence those runs
// existed was the file's creation time. The first line fixes that: it names the
// run, the intent, the writer's pid and where it was going to work.
func TestNoteRunStartedIsTheFirstJournalLine(t *testing.T) {
	s, st := newFocusTestServer(t)
	in, err := st.CreateIntent(model.Intent{Title: "started fixture"})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := st.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, s, run.ID)

	s.noteRunStarted(run.ID, in.ID, "dispatch", `C:\case\in_x`)
	s.closeJournal(run.ID)

	lines := readJournalFileLines(t, s.DataDir, run.ID)
	if len(lines) != 1 {
		t.Fatalf("journal holds %d lines, want 1: %v", len(lines), lines)
	}
	got := lines[0]
	if got["kind"] != "run_started" || got["run_id"] != run.ID || got["intent_id"] != in.ID {
		t.Fatalf("first line = %v, want run_started for %s/%s", got, run.ID, in.ID)
	}
	if got["source"] != "dispatch" || got["pid"] == nil || got["work_dir"] == nil {
		t.Fatalf("first line lacks dispatch/pid/work_dir: %v", got)
	}
}

// The ledger has carried Run.CancelCause all along, the journal did not: 29 of the
// 49 cancelled runs in the package said only "canceled  context canceled". This is
// the line that makes a cancellation attributable from the package alone.
func TestNoteRunCancelCauseRecordsTheCause(t *testing.T) {
	s, st := newFocusTestServer(t)
	in, err := st.CreateIntent(model.Intent{Title: "cancel fixture"})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := st.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, s, run.ID)
	s.Lifecycle.SetCancelCause(run.ID, model.CancelCauseWatchdog)
	s.noteRunCancelCause(run.ID)
	s.closeJournal(run.ID)

	lines := readJournalFileLines(t, s.DataDir, run.ID)
	if len(lines) != 1 || lines[0]["kind"] != "run_cancel" || lines[0]["cause"] != model.CancelCauseWatchdog {
		t.Fatalf("journal = %v, want one run_cancel with cause %q", lines, model.CancelCauseWatchdog)
	}

	// A run with no recorded cause writes nothing: an invented cause is worse than
	// no line, because the whole point of the line is attributability.
	s2, st2 := newFocusTestServer(t)
	in2, err := st2.CreateIntent(model.Intent{Title: "no cause"})
	if err != nil {
		t.Fatal(err)
	}
	_, run2, err := st2.StartIntentRun(in2.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, s2, run2.ID)
	s2.noteRunCancelCause(run2.ID)
	s2.closeJournal(run2.ID)
	if lines := readJournalFileLines(t, s2.DataDir, run2.ID); len(lines) != 0 {
		t.Fatalf("journal = %v, want no run_cancel when no cause was recorded", lines)
	}
}

// Restart cancels are written after the fact: the process that owned the journal is
// gone, so Recover appends to the file on disk instead of going through a live
// writer. 20 of the 49 cancelled runs in the package ended exactly this way.
func TestAppendJournalOnDiskRecordsARestartCancel(t *testing.T) {
	s, st := newFocusTestServer(t)
	in, err := st.CreateIntent(model.Intent{Title: "recover fixture"})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := st.StartIntentRun(in.ID, "local", "", model.Run{})
	if err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, s, run.ID)
	s.noteRunStarted(run.ID, in.ID, "dispatch", "")
	s.closeJournal(run.ID) // the process died here: no writer, file ends mid-flight

	if err := s.appendJournalOnDisk(run.ID, map[string]any{
		"kind": "run_cancel", "cause": model.CancelCauseRestart, "source": "recover",
	}); err != nil {
		t.Fatal(err)
	}
	lines := readJournalFileLines(t, s.DataDir, run.ID)
	if len(lines) != 2 {
		t.Fatalf("journal holds %d lines, want 2: %v", len(lines), lines)
	}
	last := lines[len(lines)-1]
	if last["kind"] != "run_cancel" || last["cause"] != model.CancelCauseRestart || last["source"] != "recover" {
		t.Fatalf("last line = %v, want run_cancel restart/recover", last)
	}

	// A journal that never existed can still be created: an orphan row without a log
	// is exactly the case where the reviewer has nothing else.
	orphan := run.ID + "_orphan"
	if err := os.MkdirAll(filepath.Join(s.DataDir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.appendJournalOnDisk(orphan, map[string]any{"kind": "run_cancel", "cause": model.CancelCauseRestart}); err != nil {
		t.Fatal(err)
	}
	if lines := readJournalFileLines(t, s.DataDir, orphan); len(lines) != 1 {
		t.Fatalf("orphan journal holds %d lines, want 1", len(lines))
	}
}
