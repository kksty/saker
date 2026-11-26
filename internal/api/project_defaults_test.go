package api

import (
	"context"
	"github.com/kksty/saker/internal/executor"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type usageCommanderExec struct {
	req    executor.Request
	res    executor.Result
	model  string
	resume bool
}

func (e *usageCommanderExec) Name() string { return "usage-commander" }

func (e *usageCommanderExec) Run(_ context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	e.req = req
	if e.model != "" {
		emit(executor.Event{Kind: executor.KindSession, Text: e.model})
	}
	return e.res, nil
}

func (e *usageCommanderExec) CanResume(node, workDir string) bool { return e.resume }

func commanderWithGateway(t *testing.T, ex *usageCommanderExec) (*Server, *usageCommanderExec) {
	t.Helper()
	s, _, _ := newCommanderServer(t)
	s.Exec = ex
	s.ModelSettings = func() (string, string, int) { return "openai", "gateway-model", 32000 }
	return s, ex
}

// Commander tokens are observed exactly like worker tokens.
func TestCommanderRunRecordsOnlyTokenUsage(t *testing.T) {
	ex := &usageCommanderExec{
		model: "fast-alias-resolved",
		res: executor.Result{
			ExitCode: 0, StopReason: "completed",
			Tokens: 300, InputTokens: 200, OutputTokens: 100,
			CachedInputTokens: 0, UsageRequests: 1, UsageKnown: true,
		},
	}
	s, _ := commanderWithGateway(t, ex)
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮"), false); err != nil {
		t.Fatal(err)
	}
	runs := s.Store.Runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	run := runs[0]
	if !run.Usage.Known || run.Usage.InputTokens != 200 || run.Usage.OutputTokens != 100 ||
		run.Usage.TotalTokens != 300 || run.Usage.Requests != 1 {
		t.Fatalf("usage = %+v", run.Usage)
	}
}

// The bound profile names the provider/model the round started with; the
// harness's session event corrects it to the model actually resolved.
func TestCommanderRunRecordsProviderAndModel(t *testing.T) {
	ex := &usageCommanderExec{model: "fast-actual", res: executor.Result{ExitCode: 0, StopReason: "completed"}}
	s, _ := commanderWithGateway(t, ex)
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor("## 本轮"), false); err != nil {
		t.Fatal(err)
	}
	run := s.Store.Runs()[0]
	if run.Provider != "openai" || run.Model != "fast-actual" {
		t.Fatalf("provider/model = %q/%q", run.Provider, run.Model)
	}
	if run.ResolvedProfile != "" || run.ProfileSource != "" || run.Tier != "" {
		t.Fatalf("binding = %+v", run)
	}
}

// A selected project commander model is the actual routing target.

// The round's prompt is not stored twice, but the decision to inject the whole
// note must be provable from the run log: runs/<id>.jsonl gets a kind=injected
// event when the full section rode the prompt, and nothing when only a pointer
// did (docs/17 P0-2).
func TestCommanderStateInjectionIsJournaled(t *testing.T) {
	ex := &usageCommanderExec{res: executor.Result{ExitCode: 0, StopReason: "completed"}}
	s, _, st := newCommanderServer(t)
	s.Exec = ex
	full := "## 本轮\nboard 更新了。\n## 你的笔记（state.md，全文）\n唯一标记\n\n## board\n{}\n"
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor(full), false); err != nil {
		t.Fatal(err)
	}
	run := st.Runs()[0]
	data, err := os.ReadFile(filepath.Join(s.DataDir, "runs", run.ID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	journal := string(data)
	if !strings.Contains(journal, `"kind":"injected"`) || !strings.Contains(journal, `"source":"state"`) {
		t.Fatalf("full-note injection was not journaled:\n%s", journal)
	}
	if !strings.Contains(journal, `"prompt_section":"state"`) {
		t.Fatalf("injection event has no prompt section:\n%s", journal)
	}

	// A normal continued round (pointer only) must not claim to have injected
	// the note.
	pointer := "## 本轮\nboard 更新了。\n## 你的笔记（state.md）\n本会话里已有它。\n- API：GET /api/state\n\n## board\n{}\n"
	if _, _, err := s.StartCommander(context.Background(), withBoardAnchor(pointer), false); err != nil {
		t.Fatal(err)
	}
	for _, r := range st.Runs() {
		if r.ID == run.ID {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.DataDir, "runs", r.ID+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), `"source":"state"`) {
			t.Fatalf("pointer-only prompt was journaled as a full injection:\n%s", data)
		}
	}
}
