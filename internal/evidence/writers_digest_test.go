package evidence_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/saker/internal/evidence"
	"github.com/kksty/saker/internal/ledger"
	"github.com/kksty/saker/internal/store"
	"github.com/kksty/saker/internal/traffic"
)

// A row without a digest can never be read again (evidence.ErrNoDigest), so a
// writer that forgets the digest it just computed destroys its own proof.
//
// This walks the three production writers — ledger ingest, ledger import, traffic
// capture — instead of the store API, because the invariant belongs to the code
// that writes rows: the store stays a plain recorder, and a new writer that skips
// evidence.Preserve fails here rather than producing unreadable rows later.
func TestEveryEvidenceWriterRecordsADigest(t *testing.T) {
	t.Run("ledger ingest", func(t *testing.T) {
		caseRoot := t.TempDir()
		workDir := filepath.Join(caseRoot, "in_writer")
		writeTestFile(t, filepath.Join(workDir, "proof.txt"), "declared proof bytes")
		writeTestFile(t, filepath.Join(workDir, ledger.HandoffDir, ledger.HandoffFile),
			`{"kind":"vuln","title":"写入方必须记摘要","summary":"s","evidence":["proof.txt"]}`+"\n")
		st := openTestLedger(t)
		g := &ledger.Ingestor{Store: st, MaxEvidence: 10}
		// A future `since` disables auto-harvest so this case covers the declared
		// evidence path deterministically.
		if n := g.OnFinish("r_writer", "in_writer", caseRoot, workDir, time.Now().Add(time.Hour)); n != 1 {
			t.Fatalf("declared evidence = %d, want 1", n)
		}
		assertEveryRowProves(t, st, caseRoot)
	})

	t.Run("ledger import", func(t *testing.T) {
		caseRoot := t.TempDir()
		st := openTestLedger(t)
		src := filepath.Join(t.TempDir(), "artifact.txt")
		writeTestFile(t, src, "imported bytes")
		if _, err := ledger.AttachImport(st, caseRoot, src, "raw", "导入", "操作员带来的文件"); err != nil {
			t.Fatalf("AttachImport: %v", err)
		}
		assertEveryRowProves(t, st, caseRoot)
	})

	t.Run("traffic capture", func(t *testing.T) {
		caseRoot := t.TempDir()
		st := openTestLedger(t)
		har := filepath.Join(t.TempDir(), "capture.har")
		writeTestFile(t, har, `{"log":{"version":"1.2","entries":[`+
			`{"request":{"method":"GET","url":"http://10.0.0.5:8080/admin"},"response":{"status":200}}]}}`)
		im := &traffic.Importer{Store: st, CaseRoot: caseRoot}
		res, err := im.Import(har)
		if err != nil {
			t.Fatalf("Importer.Import: %v", err)
		}
		if res.EvidenceID == "" {
			t.Fatal("traffic import recorded no evidence row")
		}
		assertEveryRowProves(t, st, caseRoot)
	})
}

// assertEveryRowProves fails when a row the writer recorded cannot be proven: it
// must carry a digest and pass the same Verify a reader uses.
func assertEveryRowProves(t *testing.T, st *store.Store, caseRoot string) {
	t.Helper()
	rows := st.Snapshot().Evidence
	if len(rows) == 0 {
		t.Fatal("writer recorded no evidence row: the case proves nothing")
	}
	for _, e := range rows {
		if e.Digest == "" {
			t.Fatalf("evidence %s (%s) was recorded without a digest", e.ID, e.URI)
		}
		// The address must be the preserved object itself: a writer that invents a
		// digest for a mutable working path would pass the check above while the
		// bytes behind it can still change.
		if want := ".evidence/objects/" + e.Digest; e.URI != want {
			t.Fatalf("evidence %s points at %q, want the preserved object %q", e.ID, e.URI, want)
		}
		if err := evidence.Verify(caseRoot, e); err != nil {
			t.Fatalf("evidence %s cannot be verified after write: %v", e.ID, err)
		}
	}
}

func openTestLedger(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
