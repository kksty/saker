package api

import (
	"encoding/json"
	"github.com/kksty/saker/internal/testutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/skills"
)

func getTools(t *testing.T, s *Server) toolsResponse {
	t.Helper()
	mux := http.NewServeMux()
	s.Register(mux)
	srv := testutil.NewServer(t, mux)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/tools")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got toolsResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestToolLeaderboardCountsCalls pins the ranking source: tool_call events in
// the run logs, not facts (a call that found nothing still cost a turn).
func TestToolLeaderboardCountsCalls(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	r1 := `
{"kind":"tool_call","tool":"bash","at":"2026-01-01T00:00:00Z"}
{"kind":"tool_result","tool":"bash","at":"2026-01-01T00:00:01Z"}
{"kind":"tool_call","tool":"bash","at":"2026-01-02T00:00:00Z"}
not json
`
	if err := os.WriteFile(filepath.Join(runs, "r1.jsonl"), []byte(r1), 0o644); err != nil {
		t.Fatal(err)
	}
	stampJournalVersion(t, filepath.Join(runs, "r1.jsonl"))
	r2 := `{"kind":"tool_call","tool":"rg","at":"2026-01-03T00:00:00Z"}
`
	if err := os.WriteFile(filepath.Join(runs, "r2.jsonl"), []byte(r2), 0o644); err != nil {
		t.Fatal(err)
	}
	stampJournalVersion(t, filepath.Join(runs, "r2.jsonl"))

	s := New(nil, nil, dir, filepath.Join(dir, "cases"))
	s.ArsenalDirs = []string{filepath.Join(dir, "no-arsenal")}
	got := getTools(t, s)

	if got.RunsScanned != 2 {
		t.Fatalf("runs_scanned = %d, want 2", got.RunsScanned)
	}
	if len(got.Leaderboard) != 2 {
		t.Fatalf("leaderboard = %+v", got.Leaderboard)
	}
	top := got.Leaderboard[0]
	if top.Tool != "bash" || top.Calls != 2 || top.Runs != 1 {
		t.Fatalf("top = %+v, want bash 2 calls in 1 run", top)
	}
	if top.LastAt == nil || top.LastAt.UTC().Format("2006-01-02") != "2026-01-02" {
		t.Fatalf("last_at = %v", top.LastAt)
	}
	if got.Leaderboard[1].Tool != "rg" || got.Leaderboard[1].Calls != 1 {
		t.Fatalf("second = %+v", got.Leaderboard[1])
	}

	// Append to a run log: the cache must notice the size change.
	f, err := os.OpenFile(filepath.Join(runs, "r1.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(currentJournalLine(t, `{"kind":"tool_call","tool":"bash","at":"2026-01-04T00:00:00Z"}`) + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	again := getTools(t, s)
	if again.Leaderboard[0].Calls != 3 {
		t.Fatalf("after append calls = %d, want 3", again.Leaderboard[0].Calls)
	}
}

// TestArsenalToolsReadsManifest: the page shows what is installed, with the
// version the arsenal manifest recorded (not guessed from the filename).
func TestArsenalToolsReadsManifest(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "arsenal")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "rg.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"rg":{"version":"15.2.0","installed_at":"2026-11-01T13:58:30+08:00","source":"bundle","managed_by":"cyber-audit"}}`
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	s := New(nil, nil, dir, filepath.Join(dir, "cases"))
	s.ArsenalDirs = []string{root}
	got := getTools(t, s)

	if len(got.Arsenal) != 1 {
		t.Fatalf("arsenal = %+v", got.Arsenal)
	}
	row := got.Arsenal[0]
	if row.Name != "rg" || row.Version != "15.2.0" || row.Source != "bundle" || row.ManagedBy != "cyber-audit" {
		t.Fatalf("row = %+v", row)
	}
	if len(row.Dirs) != 1 || row.Dirs[0] != bin {
		t.Fatalf("dirs = %v, want [%q]", row.Dirs, bin)
	}
	if len(got.ArsenalDirs) != 1 || got.ArsenalDirs[0] != bin {
		t.Fatalf("arsenal_dirs = %v", got.ArsenalDirs)
	}
	// The override is still labelled in the new source list; a file is not
	// promoted to "available" (the runtime section answers that separately).
	if len(got.ToolRoots) != 1 || got.ToolRoots[0].Path != root || got.ToolRoots[0].Source != "configured" || !got.ToolRoots[0].Exists {
		t.Fatalf("tool_roots = %+v", got.ToolRoots)
	}
	if got.Runtime == nil || got.Runtime.State != "unavailable" || got.Runtime.Error == "" {
		t.Fatalf("unreachable runtime must be explained, not silently empty: %+v", got.Runtime)
	}
}

// findToolRoot returns the root with this source, or fails.
func findToolRoot(t *testing.T, roots []toolRoot, source string) toolRoot {
	t.Helper()
	for _, root := range roots {
		if root.Source == source {
			return root
		}
	}
	t.Fatalf("tool root %q missing from %+v", source, roots)
	return toolRoot{}
}

// TestToolRootsFollowHarnessDataDirResolution pins the root fix: the console
// must name the directory the scan resolves by the harness's own rule
// (<instance>/.cyber/arsenal when it exists, otherwise ~/.cyber/arsenal), and
// must not list a legacy instance root that holds nothing.
func TestToolRootsFollowHarnessDataDirResolution(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skip("no user home directory to compare the harness fallback against")
	}
	root := t.TempDir()
	projectDir := filepath.Join(root, "projects", "default")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(nil, nil, projectDir, filepath.Join(projectDir, "cases"))

	// No <instance>/.cyber yet: the scan falls back to the user directory.
	before := getTools(t, s)
	harnessRoot := findToolRoot(t, before.ToolRoots, "harness")
	if harnessRoot.Path != filepath.Join(root, ".cyber", "arsenal") || harnessRoot.Resolved {
		t.Fatalf("harness root before .cyber exists = %+v", harnessRoot)
	}
	userRoot := findToolRoot(t, before.ToolRoots, "user")
	if want := filepath.Join(home, ".cyber", "arsenal"); userRoot.Path != want || !userRoot.Resolved {
		t.Fatalf("user root = %+v, want resolved %q", userRoot, want)
	}
	// The legacy <instance>/arsenal holds nothing, so it is not a candidate the
	// page should present as a fallback.
	for _, r := range before.ToolRoots {
		if r.Source == "instance" {
			t.Fatalf("empty legacy instance root is still listed: %+v", r)
		}
	}
	// Once an operator drops tools there it is a real root and must show up.
	legacyBin := filepath.Join(root, "arsenal", "bin")
	if err := os.MkdirAll(legacyBin, 0o755); err != nil {
		t.Fatal(err)
	}
	withLegacy := getTools(t, s)
	if got := findToolRoot(t, withLegacy.ToolRoots, "instance"); !got.Exists || filepath.Base(filepath.Dir(got.Bin)) != "arsenal" {
		t.Fatalf("legacy instance root = %+v, want an existing root", got)
	}

	// Create the harness data directory the scan uses (saker installs its
	// skills there) and drop one installed tool into its arsenal. The console
	// must report that copy from the harness root, with the source map making
	// the origin explicit.
	if _, err := skills.Install(skills.Commander, filepath.Join(root, ".cyber", "skills")); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, ".cyber", "arsenal", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "p02-fixture-tool"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".cyber", "arsenal", "manifest.json"), []byte(`{"p02-fixture-tool":{"version":"1.2.3"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	after := getTools(t, s)
	harnessRoot = findToolRoot(t, after.ToolRoots, "harness")
	if !harnessRoot.Exists || !harnessRoot.Resolved || !harnessRoot.PathInjected {
		t.Fatalf("harness root after .cyber exists = %+v", harnessRoot)
	}
	userRoot = findToolRoot(t, after.ToolRoots, "user")
	if userRoot.Resolved {
		t.Fatalf("user root still claims resolution: %+v", userRoot)
	}
	var installed *ArsenalTool
	for i := range after.Arsenal {
		if after.Arsenal[i].Name == "p02-fixture-tool" {
			installed = &after.Arsenal[i]
		}
	}
	if installed == nil || len(installed.Dirs) != 1 || installed.Dirs[0] != bin {
		t.Fatalf("installed tool = %+v, want dir %q", installed, bin)
	}
	if installed.Version != "1.2.3" {
		t.Fatalf("manifest version = %q", installed.Version)
	}
	// The legacy instance root holds a stray bin directory from this fixture, so
	// it is listed — but it is never the copy the scan resolves.
	if r := findToolRoot(t, after.ToolRoots, "instance"); r.Resolved || !r.Exists {
		t.Fatalf("legacy instance root = %+v, want an existing root that is not resolved", r)
	}
	if after.ToolRootsNote == "" {
		t.Fatal("tool_roots_note must explain how the roots were derived")
	}
}

// A fake AOP host is enough to pin the availability contract: /api/tools reads
// the host's declared commands/model, and a reachable host's empty command list
// is still reported with state=connected rather than mistaken for a file list.
func TestToolsRuntimeSectionComesFromHostDeclarations(t *testing.T) {
	host := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/cyber.rpc.agent.AgentService/ListAgents" {
			http.Error(w, "unexpected host operation", 500)
			return
		}
		_, _ = w.Write([]byte(`{"agents":[{"hello":{"nodeId":"local"},"status":{"provider":"openai","model":"fixture-model"},"commands":[{"name":"!arsenal"},{"name":"/skill:commander"}]}]}`))
	}))
	defer host.Close()
	s := New(nil, termExec{url: host.URL}, t.TempDir(), t.TempDir())
	s.ArsenalDirs = []string{filepath.Join(t.TempDir(), "arsenal")}
	got := getTools(t, s)
	if got.Runtime == nil || got.Runtime.State != "connected" || got.Runtime.Model != "fixture-model" {
		t.Fatalf("runtime = %+v", got.Runtime)
	}
	if len(got.Runtime.Commands) != 2 || got.Runtime.Commands[0].Name != "!arsenal" {
		t.Fatalf("runtime commands = %+v", got.Runtime.Commands)
	}
}

// TestExecutorToolDeclarationIsStoredVerbatim is P0-2's "declaration data"
// contract: an executor may state that a tool is unavailable or that it
// installed one itself. That statement is data (fact tags/note text and the
// handoff note). The platform records it exactly and never parses it into a
// category, a task type or a tool tutorial. If a write path ever trimmed or
// reclassified it, this test fails.
func TestExecutorToolDeclarationIsStoredVerbatim(t *testing.T) {
	s, st, work, in, run := newLiveDeclarationServer(t)
	const summary = "arsenal 不可用：本机缺少该工具；执行者已自装 /usr/local/bin/p02-fixture 并加入 PATH"
	const doc = "工具不可用/已自装。声明原文，平台不解析。"
	const tagUnavailable = "tool-unavailable"
	const tagSelfInstalled = "self-installed:p02-fixture"
	appendHandoff(t, work, `{"kind":"note","title":"工具环境声明","summary":"`+summary+`","doc":"`+doc+`","tags":["`+tagUnavailable+`","`+tagSelfInstalled+`"]}`)

	if n := s.Ingest.OnFinish(run.ID, in.ID, s.CaseRoot, work, time.Now().Add(time.Hour)); n != 1 {
		t.Fatalf("declared = %d, want 1", n)
	}
	f := findFactByTitle(t, st, in.ID, "工具环境声明")
	if f.Kind != "note" || f.Summary != summary || f.Doc != doc {
		t.Fatalf("declaration was rewritten: kind=%q summary=%q doc=%q", f.Kind, f.Summary, f.Doc)
	}
	if len(f.Tags) != 2 || f.Tags[0] != tagUnavailable || f.Tags[1] != tagSelfInstalled {
		t.Fatalf("declaration tags = %v, want [%s %s] verbatim", f.Tags, tagUnavailable, tagSelfInstalled)
	}
	// The API view used by sessions and the console carries the same tags.
	view := toFactView(f)
	if len(view.Tags) != 2 || view.Tags[0] != tagUnavailable || view.Tags[1] != tagSelfInstalled {
		t.Fatalf("fact view dropped the declaration: %+v", view.Tags)
	}
	// The statement is one note; the platform did not add its own tool advice
	// fact, and it did not turn the declaration into another kind.
	if facts := st.ListFacts(in.ID, 10); len(facts) != 1 {
		t.Fatalf("platform added facts next to the declaration: %+v", facts)
	}

	// The handoff note is the other declaration channel. It is carried into the
	// replacement executor's brief verbatim, with no parsing or classification.
	note := "工具不可用：arsenal 无该工具；已自装 p02-fixture（tag=self-installed:p02-fixture）"
	intent, ok := st.GetIntent(in.ID)
	if !ok {
		t.Fatal("intent missing")
	}
	snapshot := s.buildHandoff(intent, note)
	if snapshot.Note != note {
		t.Fatalf("handoff note rewritten: %q", snapshot.Note)
	}
	section := handoffSection(st.Snapshot(), intent, snapshot, handoffAuthorization{}, false)
	if !strings.Contains(section, note) {
		t.Fatalf("handoff brief lost the declaration: %q", section)
	}
	for _, forbidden := range []string{"工具用法", "安装步骤", "攻击步骤"} {
		if strings.Contains(section, forbidden) {
			t.Fatalf("platform added instructions next to the declaration: %q", forbidden)
		}
	}
}

// TestToolLeaderboardBucketFailureReasons pins the failure-reason aggregation:
// the journal has no error code, so a failed result's own text is normalised
// into a few stable buckets, and only the top three survive.
func TestToolLeaderboardBucketFailureReasons(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"kind":"tool_call","tool":"bash","at":"2026-01-01T00:00:00Z"}`,
		`{"kind":"tool_result","tool":"bash","ok":false,"duration_ms":10,"raw":{"tool_result":{"output":[{"text":"curl: unsupported flag --tlsv1.2"}]}}}`,
		`{"kind":"tool_call","tool":"bash","at":"2026-01-01T00:00:01Z"}`,
		`{"kind":"tool_result","tool":"bash","ok":false,"duration_ms":10,"raw":{"tool_result":{"output":[{"text":"bash: /c/Windows/curl.exe: executable file not found"}]}}}`,
		`{"kind":"tool_call","tool":"bash","at":"2026-01-01T00:00:02Z"}`,
		`{"kind":"tool_result","tool":"bash","ok":false,"duration_ms":10,"raw":{"tool_result":{"output":[{"text":"command failed\n[exit code: 1]"}]}}}`,
		`{"kind":"tool_call","tool":"bash","at":"2026-01-01T00:00:03Z"}`,
		`{"kind":"tool_result","tool":"bash","ok":false,"duration_ms":10,"raw":{"tool_result":{"output":[{"text":"command failed\n[exit code: 1]"}]}}}`,
	}
	if err := os.WriteFile(filepath.Join(runs, "r1.jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	stampJournalVersion(t, filepath.Join(runs, "r1.jsonl"))

	s := New(nil, nil, dir, filepath.Join(dir, "cases"))
	s.ArsenalDirs = []string{filepath.Join(dir, "no-arsenal")}
	got := getTools(t, s)
	if len(got.Leaderboard) != 1 {
		t.Fatalf("leaderboard = %+v", got.Leaderboard)
	}
	row := got.Leaderboard[0]
	if row.Failures != 4 {
		t.Fatalf("failures = %d, want 4", row.Failures)
	}
	labels := map[string]int{}
	for _, kind := range row.ErrorKinds {
		labels[kind.Label] = kind.Count
	}
	if labels["unsupported flag"] != 1 {
		t.Fatalf("unsupported-flag bucket = %+v", labels)
	}
	if labels["executable file not found"] != 1 {
		t.Fatalf("not-found bucket = %+v", labels)
	}
	// Three buckets, the repeated one first; ties keep a deterministic order.
	if len(row.ErrorKinds) != 3 || row.ErrorKinds[0].Label != "exit code 1" || row.ErrorKinds[0].Count != 2 {
		t.Fatalf("error kinds = %+v, want exit code 1 x2 first", row.ErrorKinds)
	}
	for i := 1; i < len(row.ErrorKinds); i++ {
		a, b := row.ErrorKinds[i-1], row.ErrorKinds[i]
		if a.Count < b.Count || (a.Count == b.Count && a.Label > b.Label) {
			t.Fatalf("error kinds not ordered: %+v", row.ErrorKinds)
		}
	}
}

func TestFailureLabelWithholdsUnrecognizedOutput(t *testing.T) {
	if got := failureLabel("", json.RawMessage(`{"tool_result":{"output":[{"text":"weird failure: socket 42"}]}}`)); got != "other failure" {
		t.Fatalf("unlabelled failure = %q", got)
	}
	if got := failureLabel("", nil); got != "other failure" {
		t.Fatalf("empty raw = %q", got)
	}
}

// The result body is the only source of a label. Reading every string leaf of
// the event picked up the envelope (id, emitted_at, turn_id, seq) in random map
// order, so the same result produced a different bucket on every scan.
func TestFailureLabelIgnoresEnvelopeAndIsStable(t *testing.T) {
	raw := json.RawMessage(`{"id":"runtime:dmxclac8bh5g:gvoi","emitted_at":"2026-11-17T08:37:15Z","session_id":"1f9d","turn_id":"turn-1794904543216133900","emitter":"local","seq":"665","tool_result":{"call_id":"call_8d","output":[{"text":{"text":"write requires a local path within 1048576 bytes"}}],"is_error":true,"name":"write"}}`)
	first := failureLabel("", raw)
	if first != "other failure" {
		t.Fatalf("label = %q", first)
	}
	for i := 0; i < 50; i++ {
		if got := failureLabel("", raw); got != first {
			t.Fatalf("scan %d label = %q, want %q", i, got, first)
		}
	}
	// Only envelope fields, no body: no label at all — never "gvoi" or "665".
	envelopeOnly := json.RawMessage(`{"id":"runtime:dmxclac8bh5g:gvoi","emitted_at":"2026-11-17T08:37:15Z","turn_id":"turn-1","emitter":"local","seq":"665","tool_result":{"call_id":"c"}}`)
	if got := failureLabel("", envelopeOnly); got != "other failure" {
		t.Fatalf("envelope leaked into a label: %q", got)
	}
	// The event's own text wins over the raw body.
	if got := failureLabel("curl: unsupported flag --tlsv1.2", raw); got != "unsupported flag" {
		t.Fatalf("event text label = %q", got)
	}
}

// The event's own ok bit decides: an explicit false is a failure, true is a
// success, and a result that omits the field is unknown and never counted —
// including one whose raw payload still carries the retired is_error.
func TestToolLeaderboardCountsFailureOnlyWhenReported(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"kind":"tool_call","tool":"bash","at":"2026-01-01T00:00:00Z"}`,
		`{"kind":"tool_result","tool":"bash","ok":false,"duration_ms":1,"text":"x"}`,
		`{"kind":"tool_call","tool":"bash","at":"2026-01-01T00:00:01Z"}`,
		`{"kind":"tool_result","tool":"bash","duration_ms":1,"raw":{"tool_result":{"output":[{"text":"x"}],"is_error":true}}}`,
		`{"kind":"tool_call","tool":"bash","at":"2026-01-01T00:00:02Z"}`,
		`{"kind":"tool_result","tool":"bash","duration_ms":1,"text":"write requires a local path"}`,
		`{"kind":"tool_call","tool":"bash","at":"2026-01-01T00:00:03Z"}`,
		`{"kind":"tool_result","tool":"bash","ok":true,"duration_ms":1,"text":"fine"}`,
	}
	if err := os.WriteFile(filepath.Join(runs, "r1.jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	stampJournalVersion(t, filepath.Join(runs, "r1.jsonl"))
	s := New(nil, nil, dir, filepath.Join(dir, "cases"))
	s.ArsenalDirs = []string{filepath.Join(dir, "no-arsenal")}
	got := getTools(t, s)
	if len(got.Leaderboard) != 1 {
		t.Fatalf("leaderboard = %+v", got.Leaderboard)
	}
	row := got.Leaderboard[0]
	if row.Completed != 4 {
		t.Fatalf("completed = %d, want 4", row.Completed)
	}
	if row.Failures != 1 {
		t.Fatalf("failures = %d, want 1 (only the explicit ok:false result)", row.Failures)
	}
	if len(row.ErrorKinds) != 1 || row.ErrorKinds[0].Label != "other failure" {
		t.Fatalf("error kinds = %+v", row.ErrorKinds)
	}
}

// A missing ok bit is unknown, not a failure: only an explicit false counts.
func TestToolFailureRequiresExplicitFalse(t *testing.T) {
	if failed(nil) {
		t.Fatal("a missing ok bit counted as a failure")
	}
	ok := true
	if failed(&ok) {
		t.Fatal("a recorded success counted as a failure")
	}
	no := false
	if !failed(&no) {
		t.Fatal("a recorded failure was not counted")
	}
}
