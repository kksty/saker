package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// graphExec reads the snapshot the way an agent would, before handing off.
type graphExec struct {
	raw    string
	prompt string
}

func (e *graphExec) Name() string { return "graph" }

func (e *graphExec) Run(ctx context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	e.prompt = req.Prompt
	raw, err := os.ReadFile(filepath.Join(req.WorkDir, filepath.FromSlash(runInputFile)))
	if err != nil {
		return executor.Result{ExitCode: -1}, err
	}
	var manifest runInputManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return executor.Result{ExitCode: -1}, err
	}
	b, err := os.ReadFile(filepath.Join(req.WorkDir, filepath.FromSlash(manifest.Graph)))
	if err != nil {
		return executor.Result{ExitCode: -1}, err
	}
	e.raw = string(b)
	line := `{"kind":"note","title":"读完图了"}` + "\n"
	if err := os.WriteFile(filepath.Join(req.WorkDir, ".tw", "facts.jsonl"), []byte(line), 0o644); err != nil {
		return executor.Result{ExitCode: -1}, err
	}
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

// The snapshot must exist before the executor starts, carry the earlier run's
// facts, and never carry the port aggregate (D58).
func TestDispatchWritesTheGraphSnapshot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Close the ledger before t.TempDir cleanup: a run's background settlement
	// write can still hold a SQLite transaction when the test body returns, and on
	// Linux that races the directory removal ("directory not empty", leftover
	// ledger.json-journal). Windows timing hides the same race.
	t.Cleanup(func() { _ = st.Close() })
	prev, err := st.CreateIntent(model.Intent{Title: "上一轮：10.0.0.5:443"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateIntent(prev.ID, func(i *model.Intent) { i.Status = model.StatusDone }); err != nil {
		t.Fatal(err)
	}
	verified := verifiedFixture(t, st, model.Fact{
		IntentID: prev.ID, Kind: "service", Title: "10.0.0.5:443", Summary: "nginx",
	})
	// An unverified observation must not ride the executor's graph: the executor
	// reads that file and acts, and no review trigger sees a belief it never
	// cites as a premise.
	if _, err := st.AddFact(model.Fact{
		IntentID: prev.ID, Kind: "service", Title: "10.0.0.5:8443", Summary: "待核验",
	}); err != nil {
		t.Fatal(err)
	}
	_ = verified

	ex := &graphExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	in, err := st.CreateIntent(model.Intent{Title: "这一轮", Description: "接着打 443"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(in.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		got, ok := st.GetIntent(in.ID)
		return ok && got.Status == model.StatusDone
	})

	if ex.raw == "" {
		t.Fatal("the executor could not read .tw/graph.json")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ex.raw), &raw); err != nil {
		t.Fatalf("snapshot is not JSON: %v\n%s", err, ex.raw)
	}
	if _, ok := raw["ports"]; ok {
		t.Fatalf("snapshot carries the port aggregate: %s", ex.raw)
	}

	var g board.Graph
	if err := json.Unmarshal([]byte(ex.raw), &g); err != nil {
		t.Fatal(err)
	}
	if g.Version != board.GraphVersion || g.GeneratedAt.IsZero() {
		t.Fatalf("header = %d / %v", g.Version, g.GeneratedAt)
	}
	var seen bool
	for _, f := range g.Facts {
		if f.Title == "10.0.0.5:8443" {
			t.Fatalf("an unverified fact rode the graph: %+v", g.Facts)
		}
		if f.Title == "10.0.0.5:443" && f.Intent == prev.ID {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("earlier run's fact missing: %+v", g.Facts)
	}
	if len(g.Intents.Running) != 1 || g.Intents.Running[0].ID != in.ID || g.Intents.Running[0].Run == "" {
		t.Fatalf("the current run is not in the snapshot as running: %+v", g.Intents.Running)
	}
	if !strings.Contains(ex.prompt, runInputFile) {
		t.Fatalf("prompt does not point at the snapshot:\n%s", ex.prompt)
	}
}
