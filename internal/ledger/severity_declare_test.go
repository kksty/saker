package ledger

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The declared grade is the first hop of the severity chain: a writer's
// `severity` line must land on the stored fact unchanged, because every later
// projection (library entry, report snapshot) copies from the fact.
func ingestSeverity(t *testing.T, body string, want int) []model.Fact {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	g := &Ingestor{Store: st, MaxEvidence: 5}
	caseRoot := t.TempDir()
	work := filepath.Join(caseRoot, "in_x")
	writeFile(t, filepath.Join(work, HandoffDir, HandoffFile), body)
	if n := g.OnFinish("run_1", "in_x", caseRoot, work, time.Now().Add(-time.Minute)); n != want {
		t.Fatalf("declared = %d, want %d", n, want)
	}
	return st.ListFacts("in_x", 0)
}

func TestDeclaredCriticalSeverityLandsOnTheFact(t *testing.T) {
	facts := ingestSeverity(t,
		`{"kind":"vuln","title":"越权读订单","severity":"critical","summary":"普通用户可读全部订单"}`+"\n", 1)
	if len(facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(facts))
	}
	if got := facts[0].Severity; got != model.SeverityCritical {
		t.Fatalf("fact severity = %q, want %q", got, model.SeverityCritical)
	}
}

// An unrecognised grade must lose the grade, not the conclusion: a typo in
// `severity` cannot delete a finding that the evidence supports.
func TestUnknownSeverityKeepsTheFactAndDropsOnlyTheGrade(t *testing.T) {
	facts := ingestSeverity(t,
		`{"kind":"vuln","title":"A","severity":"info"}`+"\n"+
			`{"kind":"vuln","title":"B","severity":"unknown"}`+"\n", 2)
	if len(facts) != 2 {
		t.Fatalf("facts = %d, want 2", len(facts))
	}
	for _, f := range facts {
		if f.Severity != "" {
			t.Fatalf("fact %s kept an illegal grade %q", f.ID, f.Severity)
		}
	}
}
