package ledger

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// A partial settlement must be a structured, persisted state, not only a
// warning: the console and the commander need the declaration identity and byte
// span, and the cursor must stay put so a replay can finish the same batch
// without running the executor again.
func TestSettlementReportsAndRecoversPendingDeclarations(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ledger.sqlite")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in, err := st.CreateIntent(model.Intent{Title: "收口失败可恢复"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: in.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	caseRoot := filepath.Join(root, "cases")
	work := filepath.Join(caseRoot, in.ID)
	if err := os.MkdirAll(filepath.Join(work, HandoffDir), 0o700); err != nil {
		t.Fatal(err)
	}
	healthy := `{"declaration_id":"kept","kind":"note","title":"已写入的一句话"}` + "\n"
	blocked := `{"declaration_id":"blocked","kind":"note","title":"被失败挡住的一句话"}` + "\n"
	if err := os.WriteFile(filepath.Join(work, HandoffDir, HandoffFile), []byte(healthy+blocked), 0o600); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER fail_blocked_fact BEFORE INSERT ON objects
		WHEN NEW.kind = 'fact' AND instr(CAST(NEW.payload AS TEXT), '"declaration_id":"blocked"') > 0
		BEGIN SELECT RAISE(ABORT,'fixture write failure'); END`); err != nil {
		t.Fatal(err)
	}

	g := &Ingestor{Store: st, MaxEvidence: 200}
	partial := g.Settle(run.ID, in.ID, caseRoot, work, time.Now().Add(-time.Minute))
	if partial.Status != model.IngestPartial || partial.Committed {
		t.Fatalf("partial outcome = %+v", partial)
	}
	if partial.Facts != 2 || partial.Declared != 2 || partial.PersistedFacts != 1 || partial.CursorBefore != 0 || partial.CursorAfter != 0 {
		t.Fatalf("partial counts = %+v", partial)
	}
	if len(partial.Pending) != 1 {
		t.Fatalf("pending = %+v", partial.Pending)
	}
	pending := partial.Pending[0]
	if pending.DeclarationID != "blocked" || pending.Location != ".saker/facts.jsonl#bytes="+
		strconv.Itoa(len(healthy))+"-"+strconv.Itoa(len(healthy)+len(blocked)) {
		t.Fatalf("pending declaration = %+v", pending)
	}
	if len(pending.Reasons) == 0 || !strings.Contains(strings.Join(pending.Reasons, "；"), "落事实") {
		t.Fatalf("pending reasons = %+v", pending.Reasons)
	}
	if _, err := os.Stat(filepath.Join(work, HandoffDir, HandoffOffset)); !os.IsNotExist(err) {
		t.Fatalf("partial settlement advanced the cursor: %v", err)
	}
	if got := len(st.ListFacts(in.ID, 0)); got != 1 {
		t.Fatalf("facts after partial settlement = %d, want 1", got)
	}
	persisted, _ := st.GetRun(run.ID)
	if persisted.Ingest == nil || persisted.Ingest.Status != model.IngestPartial || len(persisted.Ingest.Pending) != 1 {
		t.Fatalf("run row did not persist the outcome: %+v", persisted.Ingest)
	}

	// Repair: the failed write is gone, so the same batch commits. The healthy
	// declaration must not be duplicated and the blocked one must not be skipped.
	if _, err := raw.Exec("DROP TRIGGER fail_blocked_fact"); err != nil {
		t.Fatal(err)
	}
	recovered := g.Settle(run.ID, in.ID, caseRoot, work, time.Now().Add(-time.Minute))
	if recovered.Status != model.IngestSettled || !recovered.Committed || recovered.PersistedFacts != 2 || len(recovered.Pending) != 0 {
		t.Fatalf("recovered outcome = %+v", recovered)
	}
	if recovered.CursorAfter != int64(len(healthy)+len(blocked)) {
		t.Fatalf("cursor after recovery = %d", recovered.CursorAfter)
	}
	facts := st.ListFacts(in.ID, 0)
	if len(facts) != 2 {
		t.Fatalf("facts after recovery = %d, want 2 (no duplicate, no skip)", len(facts))
	}
	if runs := st.Runs(); len(runs) != 1 {
		t.Fatalf("recovery started another run: %+v", runs)
	}
	persisted, _ = st.GetRun(run.ID)
	if persisted.Ingest == nil || persisted.Ingest.Status != model.IngestSettled || !persisted.Ingest.Committed {
		t.Fatalf("run row after recovery = %+v", persisted.Ingest)
	}
}
