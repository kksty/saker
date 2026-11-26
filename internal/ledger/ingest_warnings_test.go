package ledger

// Evidence paths that cannot be registered used to be a bare count in the log.
// They are now structured warnings on the run row, so the operator — and the
// next round's prompt — can see which path to fix (task-17).

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

func newWarningLedger(t *testing.T) (*store.Store, model.Run) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.AddRun(model.Run{IntentID: "in_1", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	return st, run
}

// A batch that fails to commit must name the offending item, not only say
// "not fully persisted": the d_-instead-of-f_ playbook source was the real
// failure behind the opaque warning on the live project.
func TestUncommittedBatchNamesThePlaybookSource(t *testing.T) {
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_1")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile), `
{"kind":"playbook","title":"字典注入套件","from_fact_ids":["d_20261114_not_a_fact"],"summary":"s"}
`)
	st, run := newWarningLedger(t)
	g := &Ingestor{Store: st}
	if got := g.OnFinish(run.ID, "in_1", caseRoot, workDir, time.Now().Add(-time.Hour)); got != 1 {
		t.Fatalf("declared = %d, want the playbook line counted", got)
	}
	got, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	joined := strings.Join(got.Warnings, "\n")
	for _, want := range []string{
		"跳过打法", "字典注入套件", "d_20261114_not_a_fact",
		"不是账本事实 ID", "GET /api/facts",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warning missing %q:\n%s", want, joined)
		}
	}
	if Consumed(workDir) == 0 {
		t.Fatal("invalid playbook blocked the batch cursor")
	}
}

// Expected in-batch corrections are notes, not warnings: they must not raise
// the status page's handoff-warning count or enter the next prompt's problem
// section.
func TestRepeatedDeclarationIsInfoNotWarning(t *testing.T) {
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_1")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile), `
{"kind":"vuln","title":"同一观察","declaration_id":"obs-1","summary":"first"}
{"kind":"vuln","title":"同一观察","declaration_id":"obs-1","summary":"second"}
`)
	st, run := newWarningLedger(t)
	g := &Ingestor{Store: st}
	g.OnFinish(run.ID, "in_1", caseRoot, workDir, time.Now().Add(-time.Hour))
	got, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	if len(got.Warnings) != 0 {
		t.Fatalf("a correction became a warning: %v", got.Warnings)
	}
	joined := strings.Join(got.Infos, "\n")
	for _, want := range []string{"同 ID", "obs-1", "非错误"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("info missing %q: %v", want, got.Infos)
		}
	}
}

func TestDeclaredEvidenceWarningsAcrossThreeStates(t *testing.T) {
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_1")
	writeFile(t, filepath.Join(workDir, "ok.txt"), "proof")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile), `
{"kind":"vuln","title":"越界证据","summary":"s","evidence":["../outside.txt"]}
{"kind":"vuln","title":"彻底越界","summary":"s","evidence":["../../outside.txt"]}
{"kind":"vuln","title":"缺失证据","summary":"s","evidence":["missing.txt"]}
{"kind":"note","title":"合法证据","summary":"s","evidence":["ok.txt"]}
`)
	st, run := newWarningLedger(t)
	g := &Ingestor{Store: st}
	if got := g.OnFinish(run.ID, "in_1", caseRoot, workDir, time.Now().Add(-time.Hour)); got != 2 {
		t.Fatalf("declared = %d, want 2 kept facts (the invalid lines are dropped)", got)
	}

	run, ok := st.GetRun(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	// Two invalid paths: one resolves directly into the cases root (a file
	// there belongs to no case), the other leaves the project cases tree
	// entirely. Both are refused, and both are named on the run row; only the
	// missing file is kept (as a ref). The fourth warning is the batch-level
	// sentence: the two dropped lines are already behind the read offset, so the
	// fix is a new declaration rather than a retry.
	if len(run.Warnings) != 4 {
		t.Fatalf("warnings = %v, want one per invalid path, one for the missing file and the batch sentence", run.Warnings)
	}
	if !strings.Contains(strings.Join(run.Warnings, "\n"), "无法解析或越界已跳过") {
		t.Fatalf("warnings = %v, want the batch sentence naming the dropped lines", run.Warnings)
	}
	joined := strings.Join(run.Warnings, "\n")
	for _, want := range []string{
		"越界证据", "../outside.txt", evidenceOutOfBoundsWarning,
		"彻底越界", "../../outside.txt", evidenceOutOfBoundsWarning,
		"缺失证据", "missing.txt", "文件不存在",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warning text missing %q:\n%s", want, joined)
		}
	}
	for _, banned := range []string{"合法证据", "ok.txt"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("a valid path produced a warning (%q):\n%s", banned, joined)
		}
	}

	// The valid declaration is still evidence; the missing one is kept as a ref
	// (D44) and the invalid line was dropped. The evidence address is the
	// preserved copy, with the working path kept as provenance (D150).
	db := st.Snapshot()
	if len(db.Evidence) != 1 || db.Evidence[0].SourceURI != "in_1/ok.txt" || db.Evidence[0].Digest == "" {
		t.Fatalf("evidence = %+v", db.Evidence)
	}
}

func TestValidEvidenceProducesNoWarnings(t *testing.T) {
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_1")
	writeFile(t, filepath.Join(workDir, "ok.txt"), "proof")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile),
		`{"kind":"note","title":"都合法","summary":"s","evidence":["ok.txt"]}`)
	st, run := newWarningLedger(t)
	(&Ingestor{Store: st}).OnFinish(run.ID, "in_1", caseRoot, workDir, time.Now().Add(-time.Hour))
	run, _ = st.GetRun(run.ID)
	if len(run.Warnings) != 0 {
		t.Fatalf("valid evidence produced warnings: %v", run.Warnings)
	}
}

func TestRunWarningsAreCapped(t *testing.T) {
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_1")
	var lines strings.Builder
	for i := 0; i < model.MaxRunWarnings+5; i++ {
		lines.WriteString(`{"kind":"note","title":"事实`)
		lines.WriteString(strings.Repeat("x", i%3+1))
		lines.WriteString(itoa(i))
		lines.WriteString(`","summary":"s","evidence":["missing-`)
		lines.WriteString(itoa(i))
		lines.WriteString(`.txt"]}` + "\n")
	}
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile), lines.String())
	st, run := newWarningLedger(t)
	(&Ingestor{Store: st}).OnFinish(run.ID, "in_1", caseRoot, workDir, time.Now().Add(-time.Hour))
	run, _ = st.GetRun(run.ID)
	if len(run.Warnings) != model.MaxRunWarnings {
		t.Fatalf("warnings = %d, want the cap %d", len(run.Warnings), model.MaxRunWarnings)
	}
}

func TestMergeWarningsDedupesAndBounds(t *testing.T) {
	got := mergeWarnings([]string{"a", "b"}, []string{"b", " a ", "", "c", "c"})
	want := []string{"a", "b", "c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("merge = %v, want %v", got, want)
	}
	many := make([]string, 0, model.MaxRunWarnings+10)
	for i := 0; i < model.MaxRunWarnings+10; i++ {
		many = append(many, "w"+itoa(i))
	}
	if got := mergeWarnings(nil, many); len(got) != model.MaxRunWarnings {
		t.Fatalf("merged %d warnings, want %d", len(got), model.MaxRunWarnings)
	}
}

// A run row serialised without warnings keeps its old shape; a warning list
// written today must survive a round trip.
func TestRunWarningsJSONRoundTrip(t *testing.T) {
	old := []byte(`{"id":"r_old","intent_id":"in_1","node":"local","status":"done"}`)
	var r model.Run
	if err := json.Unmarshal(old, &r); err != nil {
		t.Fatal(err)
	}
	if r.Warnings != nil {
		t.Fatalf("an old run grew warnings: %v", r.Warnings)
	}
	raw, err := json.Marshal(model.Run{ID: "r_new", Warnings: []string{"w"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"warnings":["w"]`) {
		t.Fatalf("warnings missing from JSON: %s", raw)
	}
	raw, err = json.Marshal(model.Run{ID: "r_plain"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "warnings") {
		t.Fatalf("omitempty did not drop an empty list: %s", raw)
	}
}

// A missing citation that is one edit away from an existing file names that
// file in the warning, so the next round corrects the path instead of guessing
// it again. The hint is a lead — the fact still keeps the dangling ref.
func TestMissingEvidenceHintNamesTheNearMiss(t *testing.T) {
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_1")
	writeFile(t, filepath.Join(workDir, "req06-version.hdr"), "first")
	writeFile(t, filepath.Join(workDir, "req28-version2.hdr"), "second")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile), `
{"kind":"vuln","title":"读版本","summary":"s","evidence":["req06-version2.hdr"]}`)
	st, run := newWarningLedger(t)
	(&Ingestor{Store: st}).OnFinish(run.ID, "in_1", caseRoot, workDir, time.Now().Add(-time.Hour))
	run, _ = st.GetRun(run.ID)
	joined := strings.Join(run.Warnings, "\n")
	for _, want := range []string{
		"读版本", "req06-version2.hdr", "文件不存在",
		"最接近的现有文件是「req06-version.hdr」",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warning missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "最接近的现有文件是「req28-version2.hdr」") {
		t.Fatalf("hint pointed at the wrong request number:\n%s", joined)
	}
	db := st.Snapshot()
	found := false
	for _, f := range db.Facts {
		if f.Title != "读版本" {
			continue
		}
		found = true
		if len(f.Refs) != 1 || f.Refs[0].Locator != "req06-version2.hdr" {
			t.Fatalf("a hint must not substitute the citation: %+v", f.Refs)
		}
	}
	if !found {
		t.Fatal("declared fact missing from the snapshot")
	}
}

// Two equally close candidates are a guess, not a hint: stay silent and let the
// writer read the directory.
func TestAmbiguousMissingEvidenceHintStaysSilent(t *testing.T) {
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_1")
	writeFile(t, filepath.Join(workDir, "req06-version1.hdr"), "a")
	writeFile(t, filepath.Join(workDir, "req06-version2.hdr"), "b")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile), `
{"kind":"vuln","title":"读版本","summary":"s","evidence":["req06-version9.hdr"]}`)
	st, run := newWarningLedger(t)
	(&Ingestor{Store: st}).OnFinish(run.ID, "in_1", caseRoot, workDir, time.Now().Add(-time.Hour))
	run, _ = st.GetRun(run.ID)
	joined := strings.Join(run.Warnings, "\n")
	if !strings.Contains(joined, "req06-version9.hdr") || !strings.Contains(joined, "文件不存在") {
		t.Fatalf("the missing path must still be named:\n%s", joined)
	}
	if strings.Contains(joined, "最接近的现有文件") {
		t.Fatalf("an ambiguous near miss must not be guessed:\n%s", joined)
	}
}

// A hint must not cross extensions: the .hdr and the .html of one request are
// different artifacts, and the warning says which one is missing.
func TestMissingEvidenceHintDoesNotCrossExtensions(t *testing.T) {
	caseRoot := t.TempDir()
	workDir := filepath.Join(caseRoot, "in_1")
	writeFile(t, filepath.Join(workDir, "req06-version.html"), "body")
	writeFile(t, filepath.Join(workDir, HandoffDir, HandoffFile), `
{"kind":"vuln","title":"读版本","summary":"s","evidence":["req06-version2.hdr"]}`)
	st, run := newWarningLedger(t)
	(&Ingestor{Store: st}).OnFinish(run.ID, "in_1", caseRoot, workDir, time.Now().Add(-time.Hour))
	run, _ = st.GetRun(run.ID)
	if joined := strings.Join(run.Warnings, "\n"); strings.Contains(joined, "最接近的现有文件") {
		t.Fatalf("a .html file must not be offered for a missing .hdr:\n%s", joined)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
