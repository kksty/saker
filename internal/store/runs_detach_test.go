package store

import (
	"path/filepath"
	"testing"

	"github.com/kksty/tianwu/internal/model"
)

// Runs() must hand out rows that own their mutable fields, the same rule
// Snapshot() follows.
//
// The instruction notice path writes into a live run row's map in place
// (queueInstructionNoticesLocked takes &s.db.Runs[i] and assigns into
// r.InstructionNotices), and AcknowledgeInstruction does the same for
// r.InstructionAcks — neither goes through UpdateRun, which detaches first. A
// shallow copy therefore puts the reader and that writer on one map: a caller
// that marshals the row — GET /api/runs is exactly that — races the write, and
// the runtime's answer to "concurrent map read and map write" is a fatal error
// no recover catches, taking the whole control plane and every project's
// scheduling with it.
//
// The test pins the copy, not the race: it takes the snapshot, then lets the real
// notice path write, and asserts the earlier result did not change. It fails on
// the old copy(out, s.db.Runs) implementation.
func TestRunsReturnsDetachedRows(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.AddRun(model.Run{Purpose: "commander", Status: model.RunRunning}); err != nil {
		t.Fatal(err)
	}
	// The first durable instruction gives the live run row a notice map (the
	// queue creates it and writes into it on the live row).
	first, _, err := st.CreateInstruction(model.Instruction{
		Text: "第一条持久指令", CreatedBy: "test-operator", IdempotencyKey: "detach-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	runs := st.Runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if _, ok := runs[0].InstructionNotices[first.ID]; !ok {
		t.Fatalf("snapshot misses the notice it was taken after: %v", runs[0].InstructionNotices)
	}
	// A second instruction is a later write into the same live map.
	second, _, err := st.CreateInstruction(model.Instruction{
		Text: "第二条持久指令", CreatedBy: "test-operator", IdempotencyKey: "detach-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := runs[0].InstructionNotices[second.ID]; leaked {
		t.Fatal("Runs() shares the live row's InstructionNotices map: a reader that marshals it races the writer (fatal concurrent map read/write)")
	}
}
