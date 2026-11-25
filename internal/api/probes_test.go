package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/project"
)

// probeJournalLine is one journal line as the writer produces it: a tool call
// carries its command as the original argument text.
func probeJournalLine(at time.Time, tool, command string) string {
	line := map[string]any{
		"kind": "tool_call", "tool": tool, "call_id": "c1", "at": at.UTC().Format(time.RFC3339Nano),
		"v": executor.JournalSchemaVersion,
		"raw": map[string]any{"tool_call": map[string]any{
			"arguments": `{"command":` + probeJSONString(command) + `}`,
		}},
	}
	out, _ := json.Marshal(line)
	return string(out)
}

func probeJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func writeProbeJournal(t *testing.T, s *Server, runID string, lines ...string) {
	t.Helper()
	dir := filepath.Join(s.DataDir, "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, runID+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// probeFixture creates one direction with the given runs and no journals yet.
func probeFixture(t *testing.T, titles ...string) (*Server, *project.Project, []model.Intent, []model.Run) {
	t.Helper()
	s, p := newBindingServer(t)
	t.Cleanup(func() { s.CancelAll(); s.WaitIdle(3 * time.Second) })
	var intents []model.Intent
	var runs []model.Run
	for _, title := range titles {
		in, err := p.Store.CreateIntent(model.Intent{Title: title, Description: "d"})
		if err != nil {
			t.Fatal(err)
		}
		run, err := p.Store.AddRun(model.Run{IntentID: in.ID, Node: "local"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Store.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
			t.Fatal(err)
		}
		intents = append(intents, in)
		runs = append(runs, run)
	}
	return s, p, intents, runs
}

func TestProbeTableListsThisDirectionsProbesWithTheCommandItRan(t *testing.T) {
	s, p, intents, runs := probeFixture(t, "本意图", "别的意图")
	base := time.Date(2026, 11, 20, 18, 22, 0, 0, time.UTC)
	// A second run of the same direction: "probed N times" counts runs, so this
	// is what makes the address count 2 rather than 1.
	later, err := p.Store.AddRun(model.Run{IntentID: intents[0].ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateRun(later.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	writeProbeJournal(t, s, runs[0].ID,
		probeJournalLine(base, "bash", "curl -sS -D- http://113.219.171.189:9012/lsdd/api | head"),
		// The same address twice in one run is still one run that has seen it.
		probeJournalLine(base.Add(time.Minute), "bash", "curl -o /dev/null 113.219.171.189:9012/admin"),
		// A self-call is not a target probe, and neither is the wildcard.
		probeJournalLine(base, "bash", "curl http://127.0.0.1:8722/api/board"),
		probeJournalLine(base, "bash", "curl http://0.0.0.0:80/"),
		// Another tool's command text is not the bash journal entry this table reads.
		probeJournalLine(base, "http", "GET http://120.253.17.48:32000/x"),
	)
	writeProbeJournal(t, s, later.ID,
		probeJournalLine(base.Add(90*time.Minute), "bash", "nuclei -u 113.219.171.189:9012 -tags cve"),
	)
	writeProbeJournal(t, s, runs[1].ID,
		probeJournalLine(base.Add(2*time.Hour), "bash", "curl -sS http://113.219.171.189:9012/"),
	)

	section := s.probeSection(intents[0], p.Store.Snapshot())
	for _, want := range []string{
		probeTableTitle,
		"113.219.171.189:9012 — 探过 2 次，最近 run " + later.ID,
		"原命令首行：nuclei -u 113.219.171.189:9012 -tags cve",
		"另有 1 个意图也探过",
		probeTableFooter,
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("probe table missing %q:\n%s", want, section)
		}
	}
	if strings.Contains(section, "127.0.0.1") || strings.Contains(section, "0.0.0.0") {
		t.Fatalf("self-calls leaked into the table:\n%s", section)
	}
	if strings.Contains(section, "120.253.17.48:32000") {
		t.Fatalf("a non-bash tool call became a probe:\n%s", section)
	}
}

func TestProbeTableKeepsTheNewestEntriesAndSaysHowManyAreHidden(t *testing.T) {
	s, p, intents, runs := probeFixture(t, "本意图")
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var lines []string
	for i := 0; i < probeTableMax+5; i++ {
		lines = append(lines, probeJournalLine(base.Add(time.Duration(i)*time.Minute), "bash",
			fmt.Sprintf("curl -sS http://10.0.0.%d:8080/", i+1)))
	}
	writeProbeJournal(t, s, runs[0].ID, lines...)

	section := s.probeSection(intents[0], p.Store.Snapshot())
	if got := strings.Count(section, " — 探过 "); got != probeTableMax {
		t.Fatalf("listed %d addresses, want %d:\n%s", got, probeTableMax, section)
	}
	if !strings.Contains(section, fmt.Sprintf("还有 %d 个更早探过的地址未列出", 5)) {
		t.Fatalf("the hidden count is missing:\n%s", section)
	}
	// Newest first: the last address written must be listed, the first must not.
	first := "- 10.0.0.1:8080"
	last := fmt.Sprintf("- 10.0.0.%d:8080", probeTableMax+5)
	if strings.Contains(section, first) {
		t.Fatalf("the oldest entry survived the cap:\n%s", section)
	}
	if !strings.Contains(section, last) {
		t.Fatalf("the newest entry is missing:\n%s", section)
	}
}

// The table belongs to the executor's task book and nowhere else. The plain
// BuildPrompt path (used by the collaboration brief) passes no probes, which is
// what keeps another prompt from acquiring an excerpt of somebody's commands.
func TestProbeTableReachesOnlyTheExecutorTaskBook(t *testing.T) {
	s, p, intents, runs := probeFixture(t, "本意图")
	writeProbeJournal(t, s, runs[0].ID,
		probeJournalLine(time.Now().UTC(), "bash", "curl -sS http://113.219.171.189:9012/"))

	db := p.Store.Snapshot()
	executor := buildDispatchPrompt(db, intents[0], "10.0.0.0/8", "", "", 0, false, false, "", s.probeSection(intents[0], db), false)
	if !strings.Contains(executor, probeTableTitle) {
		t.Fatal("the executor task book lost the probe table")
	}
	// Rendered once, after the authorization boundary and before the intent.
	if strings.Count(executor, probeTableTitle) != 1 {
		t.Fatalf("the title appears %d times", strings.Count(executor, probeTableTitle))
	}
	if table, intent := strings.Index(executor, probeTableTitle), strings.Index(executor, "## 交接（必做）"); table < 0 || intent < 0 || table > intent {
		t.Fatalf("the probe table must sit before the handoff contract (table=%d handoff=%d):\n%s", table, intent, executor)
	}
	other := BuildPrompt(db, intents[0], "10.0.0.0/8", "", "", 0, false)
	if strings.Contains(other, probeTableTitle) {
		t.Fatalf("a non-executor prompt carried the probe table:\n%s", other)
	}
}

func TestProbeTableIsAbsentWhenNothingWasProbed(t *testing.T) {
	s, p, intents, _ := probeFixture(t, "本意图")
	if section := s.probeSection(intents[0], p.Store.Snapshot()); section != "" {
		t.Fatalf("a direction with no journals got a section:\n%s", section)
	}
}
