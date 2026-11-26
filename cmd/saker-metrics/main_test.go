package main

// saker-metrics tests: the read-only guarantee, the stable JSON envelope, and the
// tolerant sidecar parsing. They build a temporary ledger through the real store
// so the immutable reader is exercised against a genuine SQLite file; nothing
// here reads data/.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/metrics"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// buildProject creates <dir>/ledger.sqlite with a small committed ledger and
// returns the project directory. It closes the store, so the file is static from
// the tool's point of view.
func buildProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.sqlite"))
	if err != nil {
		t.Fatalf("open temp ledger: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	in, err := st.CreateIntent(model.Intent{Title: "验证边界", Description: "固定夹具"})
	if err != nil {
		t.Fatalf("create intent: %v", err)
	}
	if _, err := st.AddFact(model.Fact{
		ID: "f_1", IntentID: in.ID, Kind: "host", Title: "10.0.0.5:8080",
		Summary: "固定摘要", Status: "unverified", Confidence: "observed",
	}); err != nil {
		t.Fatalf("add fact: %v", err)
	}
	r, err := st.AddRun(model.Run{IntentID: in.ID, Purpose: "commander", Node: "local"})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	end := time.Now()
	if _, err := st.UpdateRun(r.ID, func(run *model.Run) {
		run.Status = model.RunDone
		run.EndedAt = &end
		run.ExitReason = "completed"
	}); err != nil {
		t.Fatalf("settle run: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close temp ledger: %v", err)
	}
	return dir
}

// dirFingerprint hashes every file name and byte in dir (one level; the tool
// only ever reads/writes there).
func dirFingerprint(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			out[e.Name()] = "dir"
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		sum := sha256.Sum256(raw)
		out[e.Name()] = hex.EncodeToString(sum[:])
	}
	return out
}

func sameFingerprint(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestRunIsReadOnlyAndLeavesNoSideFiles(t *testing.T) {
	dir := buildProject(t)
	before := dirFingerprint(t, dir)
	var out, errOut bytes.Buffer
	if code := run([]string{"--project", dir, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errOut.String())
	}
	after := dirFingerprint(t, dir)
	if !sameFingerprint(before, after) {
		t.Fatalf("source project changed:\nbefore=%v\nafter=%v", before, after)
	}
	for _, name := range []string{"ledger.sqlite-wal", "ledger.sqlite-shm"} {
		if _, ok := after[name]; ok {
			t.Fatalf("read created %s next to the source ledger", name)
		}
	}
	var env Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if !env.SourceUnchanged || len(env.ChangedFiles) != 0 {
		t.Fatalf("source_unchanged=%t changed=%v", env.SourceUnchanged, env.ChangedFiles)
	}
	if env.ReadMode != "direct_immutable" && env.ReadMode != "temp_copy" {
		t.Fatalf("unexpected read mode %q", env.ReadMode)
	}
	if len(env.Metrics) != 13 {
		t.Fatalf("metrics = %d, want 7 vertical + 6 capability-use metrics", len(env.Metrics))
	}
	if env.Counts.Facts != 1 || env.Counts.Intents != 1 || env.Counts.Runs != 1 {
		t.Fatalf("counts = %+v", env.Counts)
	}
}

func TestJSONOutputIsStable(t *testing.T) {
	dir := buildProject(t)
	var first, firstErr bytes.Buffer
	if code := run([]string{"--project", dir, "--json"}, &first, &firstErr); code != 0 {
		t.Fatalf("first run exit %d: %s", code, firstErr.String())
	}
	var second, secondErr bytes.Buffer
	if code := run([]string{"--project", dir, "--json"}, &second, &secondErr); code != 0 {
		t.Fatalf("second run exit %d: %s", code, secondErr.String())
	}
	if first.String() != second.String() {
		t.Fatalf("JSON output is not stable:\n%s\n---\n%s", first.String(), second.String())
	}
	var env Envelope
	if err := json.Unmarshal(first.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for i, m := range env.Metrics {
		if m.Order != i+1 {
			t.Fatalf("metric order = %d at index %d", m.Order, i)
		}
	}
}

func TestHumanOutputListsEveryMetric(t *testing.T) {
	dir := buildProject(t)
	var out, errOut bytes.Buffer
	if code := run([]string{"--project", dir}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, key := range []string{
		metrics.KeyHandoffReceiptValidRate,
		metrics.KeyOperatorInstructionConsumptionLatency,
		metrics.KeyFactsAfterStop,
		metrics.KeyUnconsumedVerifiedFacts,
		metrics.KeyDirectionStallRate,
		metrics.KeyEnvironmentMisjudgementRatio,
		metrics.KeyReviewQueueAndExecutionMS,
		metrics.KeyEvalPassRate,
		metrics.KeyEvalRoundsMax,
		metrics.KeyEvalRoundsAvg,
		metrics.KeyGuardrailActions,
		metrics.KeyTrafficBodyCoverage,
		metrics.KeyMediaPreserved,
	} {
		if !strings.Contains(out.String(), key) {
			t.Fatalf("human output missing %s:\n%s", key, out.String())
		}
	}
	if !strings.Contains(out.String(), "source_unchanged=true") {
		t.Fatalf("human output does not report source_unchanged:\n%s", out.String())
	}
}

func TestReadDecisionsToleratesMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "decisions.jsonl")
	got, present, malformed, warnings := readDecisions(missing)
	if got != nil || present || malformed != 0 || len(warnings) != 0 {
		t.Fatalf("missing file = %v %t %d %v", got, present, malformed, warnings)
	}

	path := filepath.Join(dir, "decisions.jsonl")
	content := `{"run_id":"r_1","handoff":true}
{"run_id":"r_2","actions":[{"kind":"intent","id":"in_1"}]}

not-json
{"run_id":"r_3","handoff":true` // trailing partial line from a crash
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, present, malformed, _ = readDecisions(path)
	if !present || len(got) != 2 || malformed != 2 {
		t.Fatalf("parsed=%d present=%t malformed=%d", len(got), present, malformed)
	}
	if !got[0].Handoff || got[0].RunID != "r_1" {
		t.Fatalf("first decision = %+v", got[0])
	}
	if len(got[1].Actions) != 1 || got[1].Actions[0].Kind != "intent" {
		t.Fatalf("second decision = %+v", got[1])
	}
}

func TestReadOperatorScopeToleratesMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "operator-scope.jsonl")
	got, present, malformed, _ := readOperatorScope(missing)
	if got != nil || present || malformed != 0 {
		t.Fatalf("missing file = %v %t %d", got, present, malformed)
	}
	path := filepath.Join(dir, "operator-scope.jsonl")
	content := `{"v":1,"id":"os_1","kind":"stop","at":"2026-01-02T03:04:05Z","actor":"operator","entity_refs":["host:10.0.0.5"]}
{"v":1,"id":"os_2","kind":"authorization","at":"2026-01-02T03:04:05Z"}
broken`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, present, malformed, _ = readOperatorScope(path)
	if !present || len(got) != 2 || malformed != 1 {
		t.Fatalf("parsed=%d present=%t malformed=%d", len(got), present, malformed)
	}
	if got[0].Kind != "stop" || len(got[0].EntityRefs) != 1 || got[0].EntityRefs[0] != "host:10.0.0.5" {
		t.Fatalf("stop entry = %+v", got[0])
	}
}

func TestMissingLedgerFails(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"--project", dir}, &out, &errOut); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "账本不可读") {
		t.Fatalf("stderr = %s", errOut.String())
	}
}

func TestUsageErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 2 {
		t.Fatalf("missing --project exit = %d", code)
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"--project", "x", "extra"}, &out, &errOut); code != 2 {
		t.Fatalf("positional arg exit = %d", code)
	}
}

func TestTempCopyFallbackReadsCommittedRows(t *testing.T) {
	dir := buildProject(t)
	ledger := filepath.Join(dir, "ledger.sqlite")
	db, err := readLedgerCopy(ledger)
	if err != nil {
		t.Fatalf("copy read: %v", err)
	}
	if len(db.Facts) != 1 || len(db.Runs) != 1 || db.Version != store.CurrentSchemaVersion() {
		t.Fatalf("copy read = %+v", db)
	}
	// The source directory still has exactly the files it had before the copy.
	if _, err := os.Stat(ledger + "-wal"); err == nil {
		if info, _ := os.Stat(ledger + "-wal"); info.Size() > 0 {
			t.Fatalf("copy read left source WAL data")
		}
	}
}

func TestLedgerNeedsCopy(t *testing.T) {
	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger.sqlite")
	if err := os.WriteFile(ledger, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ledgerNeedsCopy(ledger) {
		t.Fatal("no WAL file should not require a copy")
	}
	if err := os.WriteFile(ledger+"-wal", []byte("committed rows"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ledgerNeedsCopy(ledger) {
		t.Fatal("non-empty WAL should require a copy")
	}
}

// ---- capability-use sources (P3-H) ---------------------------------------

// buildCapabilityProject seeds the temp project with a synthetic run journal
// and HTTP capture index on top of the real temp ledger, so the CLI path that
// reads runs/*.jsonl and cases/.http/*.json is exercised end to end.
func buildCapabilityProject(t *testing.T) string {
	t.Helper()
	dir := buildProject(t)
	runsDir := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	journal := strings.Join([]string{
		`{"kind":"status","state":"eval_start","eval_round":1,"eval_max_rounds":3,"at":"2026-01-02T03:00:00Z"}`,
		`{"kind":"status","state":"eval_end","eval_round":2,"eval_max_rounds":3,"eval_pass":true,"eval_reason":"通过","at":"2026-01-02T03:01:00Z"}`,
		`{"kind":"media","text":".tw/media/e-1.png","raw":{"path":".tw/media/e-1.png","mime":"image/png","bytes":1234},"at":"2026-01-02T03:02:00Z"}`,
		`{"kind":"guardrail","action":"pending_read","session_id":"s1","at":"2026-01-02T03:03:00Z"}`,
		`{"kind":"guardrail","action":"resolve","approve":true,"session_id":"s1","at":"2026-01-02T03:04:00Z"}`,
		// The harness guardrail runtime's own structured verdicts (P4-J).
		`{"kind":"guardrail_decision","guardrail_action":"block","guardrail_tool":"bash","guardrail_reason":"命令超出白名单","at":"2026-01-02T03:05:00Z"}`,
		`{"kind":"guardrail_review","guardrail_state":"approved","guardrail_action":"review","guardrail_tool":"bash","guardrail_operation_id":"op-1","at":"2026-01-02T03:06:00Z"}`,
		`not-json trailing line from a crash`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(runsDir, "r_synth.jsonl"), []byte(journal), 0o600); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	httpDir := filepath.Join(dir, "cases", ".http")
	if err := os.MkdirAll(httpDir, 0o755); err != nil {
		t.Fatalf("mkdir .http: %v", err)
	}
	for name, body := range map[string]string{
		"a.json": `{"ref":"r_synth/f1","run_id":"r_synth","method":"GET","url":"https://example.test/a","status":200,"complete":true,"preview_only":true,"truncated":true,"request_bytes":5,"response_bytes":10}`,
		"b.json": `{"ref":"r_synth/f2","run_id":"r_synth","method":"GET","url":"https://example.test/b","status":200,"complete":true,"preview_only":false,"truncated":false,"request_bytes":7,"response_bytes":9}`,
		"c.json": `{"ref":"r_synth/f3","run_id":"r_synth","method":"GET","url":"https://example.test/c","status":200,"complete":true,"request_bytes":1,"response_bytes":2}`,
		"d.json": `not-json`,
	} {
		if err := os.WriteFile(filepath.Join(httpDir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write capture %s: %v", name, err)
		}
	}
	// Files outside the read extensions must be ignored (and not fingerprinted).
	if err := os.WriteFile(filepath.Join(runsDir, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}
	return dir
}

// treeFingerprint hashes every file below dir, recursively, in a stable shape.
func treeFingerprint(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			out[rel] = "dir"
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

func metricByKey(t *testing.T, ms []metrics.Metric, key string) metrics.Metric {
	t.Helper()
	for _, m := range ms {
		if m.Key == key {
			return m
		}
	}
	t.Fatalf("metric %q missing", key)
	return metrics.Metric{}
}

func metricDetail(t *testing.T, m metrics.Metric, name string) float64 {
	t.Helper()
	for _, d := range m.Details {
		if d.Name == name {
			return d.Value
		}
	}
	t.Fatalf("detail %q missing in %s: %v", name, m.Key, m.Details)
	return 0
}

func TestCapabilitySourcesReadEndToEndReadOnly(t *testing.T) {
	dir := buildCapabilityProject(t)
	before := treeFingerprint(t, dir)
	var out, errOut bytes.Buffer
	if code := run([]string{"--project", dir, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errOut.String())
	}
	after := treeFingerprint(t, dir)
	if !sameFingerprint(before, after) {
		t.Fatalf("capability read changed the project:\nbefore=%v\nafter=%v", before, after)
	}
	var env Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if env.Schema != "saker.metrics/v1" || env.MetricsSchema != metrics.SchemaVersion {
		t.Fatalf("versions = envelope %q metrics %d", env.Schema, env.MetricsSchema)
	}
	if !env.SourceUnchanged || len(env.ChangedFiles) != 0 {
		t.Fatalf("source_unchanged=%t changed=%v", env.SourceUnchanged, env.ChangedFiles)
	}
	c := env.Counts
	if c.RunJournals != 1 || c.JournalEvents != 7 || c.JournalMalformed != 1 || !c.RunJournalsPresent {
		t.Fatalf("journal counts = %+v", c)
	}
	if c.Captures != 3 || c.CapturesMalformed != 1 || !c.CapturesPresent {
		t.Fatalf("capture counts = %+v", c)
	}

	pass := metricByKey(t, env.Metrics, metrics.KeyEvalPassRate)
	if pass.Unavailable != "" || pass.Value == nil || *pass.Value != 1 || pass.Denominator != 1 {
		t.Fatalf("eval_pass_rate = %+v", pass)
	}
	maxR := metricByKey(t, env.Metrics, metrics.KeyEvalRoundsMax)
	if maxR.Value == nil || *maxR.Value != 2 || maxR.Denominator != 1 {
		t.Fatalf("eval_rounds_max = %+v", maxR)
	}
	avgR := metricByKey(t, env.Metrics, metrics.KeyEvalRoundsAvg)
	if avgR.Value == nil || *avgR.Value != 2 {
		t.Fatalf("eval_rounds_avg = %+v", avgR)
	}
	g := metricByKey(t, env.Metrics, metrics.KeyGuardrailActions)
	if g.Value == nil || *g.Value != 4 || g.Denominator != 1 ||
		g.Breakdown["pending_read"] != 1 || g.Breakdown["resolve_approve"] != 1 ||
		g.Breakdown["decision_block"] != 1 || g.Breakdown["review_approved"] != 1 {
		t.Fatalf("guardrail_actions = %+v", g)
	}
	if metricDetail(t, g, "journal_guardrail_events") != 2 || metricDetail(t, g, "decision_events") != 1 || metricDetail(t, g, "review_events") != 1 {
		t.Fatalf("guardrail event family details = %v", g.Details)
	}
	tc := metricByKey(t, env.Metrics, metrics.KeyTrafficBodyCoverage)
	if tc.Value == nil || *tc.Value != 0.5 || tc.Numerator != 1 || tc.Denominator != 2 {
		t.Fatalf("traffic_body_coverage = %+v", tc)
	}
	if metricDetail(t, tc, "captures_total") != 3 || metricDetail(t, tc, "preview_flag_missing") != 1 || metricDetail(t, tc, "malformed_index_rows") != 1 {
		t.Fatalf("traffic coverage details = %v", tc.Details)
	}
	media := metricByKey(t, env.Metrics, metrics.KeyMediaPreserved)
	if media.Value == nil || *media.Value != 1 || metricDetail(t, media, "media_total_bytes") != 1234 {
		t.Fatalf("media_preserved = %+v", media)
	}
}

func TestCapabilitySourcesMissingAreAbsentNotErrors(t *testing.T) {
	// The plain ledger project has neither runs/ nor cases/.http/: the readers
	// must report absence and the metrics must fall back to unavailable.
	dir := buildProject(t)
	var out, errOut bytes.Buffer
	if code := run([]string{"--project", dir, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errOut.String())
	}
	var env Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Counts.RunJournalsPresent || env.Counts.CapturesPresent || env.Counts.RunJournals != 0 || env.Counts.Captures != 0 {
		t.Fatalf("absent counts = %+v", env.Counts)
	}
	for key, want := range map[string]string{
		metrics.KeyEvalPassRate:        "no_eval_signal",
		metrics.KeyGuardrailActions:    "no_guardrail_signal",
		metrics.KeyTrafficBodyCoverage: "no_traffic_signal",
	} {
		m := metricByKey(t, env.Metrics, key)
		if m.Unavailable != want || m.Value != nil {
			t.Fatalf("%s = %+v, want unavailable(%s)", key, m, want)
		}
	}
	media := metricByKey(t, env.Metrics, metrics.KeyMediaPreserved)
	if media.Unavailable != "" || media.Value == nil || *media.Value != 0 || media.Explanation == "" {
		t.Fatalf("media_preserved without journals = %+v", media)
	}
}

func TestReadRunJournalsToleratesMalformedAndMissing(t *testing.T) {
	if js, present, warnings := readRunJournals(t.TempDir()); js != nil || present || len(warnings) != 0 {
		t.Fatalf("missing runs dir = %v %t %v", js, present, warnings)
	}
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"kind":"status","state":"eval_end","eval_pass":false}
{"kind":"message","text":"ignored"}
broken
{"kind":"media","raw":{"bytes":5}}
{"kind":"guardrail_decision","guardrail_action":"block","guardrail_tool":"bash"}
{"kind":"guardrail_review","guardrail_state":"rejected","guardrail_operation_id":"op-9"}`
	if err := os.WriteFile(filepath.Join(runsDir, "r_1.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	js, present, warnings := readRunJournals(dir)
	if !present || len(warnings) != 0 || len(js) != 1 {
		t.Fatalf("journals = %+v present=%t warnings=%v", js, present, warnings)
	}
	j := js[0]
	if j.RunID != "r_1" || j.Lines != 5 || j.Malformed != 1 || len(j.Events) != 4 {
		t.Fatalf("parsed journal = %+v", j)
	}
	if j.Events[0].EvalPass == nil || *j.Events[0].EvalPass {
		t.Fatalf("structured verdict not decoded: %+v", j.Events[0])
	}
	if j.Events[2].GuardrailAction != "block" || j.Events[3].GuardrailState != "rejected" {
		t.Fatalf("guardrail verdict fields not decoded: %+v", j.Events[2:])
	}
}

func TestReadTrafficCapturesToleratesMalformedAndMissing(t *testing.T) {
	if cs, present, malformed, warnings := readTrafficCaptures(t.TempDir()); cs != nil || present || malformed != 0 || len(warnings) != 0 {
		t.Fatalf("missing .http dir = %v %t %d %v", cs, present, malformed, warnings)
	}
	dir := t.TempDir()
	httpDir := filepath.Join(dir, "cases", ".http")
	if err := os.MkdirAll(httpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(httpDir, "a.json"), []byte(`{"ref":"r/f1","preview_only":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(httpDir, "bad.json"), []byte(`not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(httpDir, "body.pb"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	cs, present, malformed, warnings := readTrafficCaptures(dir)
	if !present || len(warnings) != 0 || malformed != 1 || len(cs) != 1 {
		t.Fatalf("captures = %+v present=%t malformed=%d warnings=%v", cs, present, malformed, warnings)
	}
	if cs[0].PreviewOnly == nil || !*cs[0].PreviewOnly {
		t.Fatalf("preview flag not decoded: %+v", cs[0])
	}
}
