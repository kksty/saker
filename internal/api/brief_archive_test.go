package api

// A new dispatch archives the mission text it was actually given under the
// case's .saker/briefs. Resumes do not rewrite it (there is nothing new to
// archive), retries keep one file per run, the file never becomes evidence, and
// known credentials are redacted before it lands on disk (task-25).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
)

type briefExec struct {
	mu      sync.Mutex
	prompts []string
	fail    bool
}

func (e *briefExec) Name() string { return "brief" }

func (e *briefExec) Run(_ context.Context, req executor.Request, emit func(executor.Event)) (executor.Result, error) {
	e.mu.Lock()
	e.prompts = append(e.prompts, req.Prompt)
	e.mu.Unlock()
	emit(executor.Event{Kind: executor.KindSession, Text: "actual-model"})
	if e.fail {
		return executor.Result{ExitCode: 1, Output: "boom"}, errors.New("boom")
	}
	return executor.Result{ExitCode: 0, StopReason: "completed"}, nil
}

func (e *briefExec) promptAt(i int) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i >= len(e.prompts) {
		return ""
	}
	return e.prompts[i]
}

// count is the locked way for a waitPoll closure to see whether the run reached
// the executor. Reading e.prompts directly is a data race: Run appends from the
// dispatch goroutine while the test polls.
func (e *briefExec) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.prompts)
}

func TestFreshDispatchArchivesBrief(t *testing.T) {
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	ex := &briefExec{}
	s.Exec = ex
	in, err := p.Store.CreateIntent(model.Intent{Title: "验证 /admin", Description: "只读"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, runID)
	run, ok := p.Store.GetRun(runID)
	if !ok {
		t.Fatal("run missing")
	}
	if run.BriefPath == "" || !strings.Contains(run.BriefPath, ".saker/briefs/"+runID+".md") {
		t.Fatalf("brief path = %q", run.BriefPath)
	}
	abs := filepath.Join(p.CaseDir, filepath.FromSlash(run.BriefPath))
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("archived brief missing: %v", err)
	}
	if string(data) != ex.promptAt(0) {
		t.Fatalf("archived brief is not the dispatched prompt:\nfile=%q\nprompt=%q", data, ex.promptAt(0))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(abs)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("brief mode = %o, want 600", info.Mode().Perm())
		}
	}

	// /api/runs exposes the field.
	runs := call(t, s, http.MethodGet, "/api/runs", nil)
	if !strings.Contains(runs.Body.String(), `"brief_path"`) || !strings.Contains(runs.Body.String(), runID) {
		t.Fatalf("/api/runs does not expose brief_path: %s", runs.Body.String())
	}
	// The archive is operator material, not evidence: the ingestor skips .saker.
	for _, e := range p.Store.Snapshot().Evidence {
		if strings.Contains(e.URI, ".saker/") {
			t.Fatalf("ingest harvested a .saker file as evidence: %+v", e)
		}
	}
}

// A resume carries the current brief plus its continuation, and archives
// exactly that: the file answers "what did this run believe it was asked", and a
// resumed run's answer is not the session's older copy. A retry keeps its own
// file next to the old one.
func TestResumeArchivesTheBriefItActuallyCarried(t *testing.T) {
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	ex := &briefExec{fail: true}
	s.Exec = ex
	in, err := p.Store.CreateIntent(model.Intent{Title: "意图"})
	if err != nil {
		t.Fatal(err)
	}
	run1ID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, run1ID)
	run1, _ := p.Store.GetRun(run1ID)
	if run1.BriefPath == "" {
		t.Fatal("the first dispatch did not archive its brief")
	}

	// Resume path: prompt != "" -> keepPrompt=true. The rebuild still happens,
	// so the run receives and archives a brief of its own.
	resumeID, err := s.startRunWith(in.ID, "## 续话\n继续原来的意图", run1ID, true, true)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, resumeID)
	resumeRun, _ := p.Store.GetRun(resumeID)
	if resumeRun.BriefPath == "" || resumeRun.BriefPath == run1.BriefPath {
		t.Fatalf("resume brief path = %q (first %q)", resumeRun.BriefPath, run1.BriefPath)
	}
	archived, err := os.ReadFile(filepath.Join(p.CaseDir, filepath.FromSlash(resumeRun.BriefPath)))
	if err != nil {
		t.Fatalf("resume brief missing: %v", err)
	}
	for _, want := range []string{continuationBriefHeader, "## 续话", "## 交接（必做）"} {
		if !strings.Contains(string(archived), want) {
			t.Fatalf("resume brief lacks %q:\n%s", want, archived)
		}
	}

	// Fresh retry: a second dispatch archives a second file.
	run2ID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, run2ID)
	run2, _ := p.Store.GetRun(run2ID)
	if run2.BriefPath == "" || run2.BriefPath == run1.BriefPath {
		t.Fatalf("retry brief path = %q (first %q)", run2.BriefPath, run1.BriefPath)
	}
	for _, rel := range []string{run1.BriefPath, run2.BriefPath} {
		if _, err := os.Stat(filepath.Join(p.CaseDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("archived brief %q missing: %v", rel, err)
		}
	}
	if ex.promptAt(0) == ex.promptAt(1) {
		t.Fatal("the two fresh dispatches had the same prompt; the fixture does not exercise multiple files")
	}
}

// The policy: a continued session must see the task as it is now, not the copy
// it was started with. Changing the direction between runs must show up in the
// continuation prompt, marked as authoritative over history.
func TestResumeSendsTheCurrentBriefNotTheSessionCopy(t *testing.T) {
	s, p := newBindingServer(t)
	if err := p.SetNodeBinding([]string{"local"}, "local"); err != nil {
		t.Fatal(err)
	}
	ex := &briefExec{fail: true}
	s.Exec = ex
	in, err := p.Store.CreateIntent(model.Intent{Title: "意图", Eval: "旧验收：拿到 200 响应"})
	if err != nil {
		t.Fatal(err)
	}
	run1ID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, run1ID)
	if !strings.Contains(ex.promptAt(0), "旧验收") {
		t.Fatalf("fixture: first dispatch lacks the acceptance text:\n%s", ex.promptAt(0))
	}

	// The direction moves while the session is idle (operator/commander edit).
	if _, err := p.Store.UpdateIntent(in.ID, func(i *model.Intent) { i.Eval = "新验收：拿到导出文件的哈希" }); err != nil {
		t.Fatal(err)
	}
	resumeID, err := s.startRunWith(in.ID, "继续原来的意图", run1ID, true, true)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, p, resumeID)
	prompt := ex.promptAt(1)
	if !strings.HasPrefix(prompt, continuationBriefHeader) {
		t.Fatalf("continuation prompt does not lead with the authoritative header:\n%s", prompt)
	}
	if !strings.Contains(prompt, "新验收：拿到导出文件的哈希") {
		t.Fatalf("continuation prompt lacks the current acceptance text:\n%s", prompt)
	}
	if strings.Contains(prompt, "旧验收：拿到 200 响应") {
		t.Fatalf("continuation prompt re-sent the stale acceptance text:\n%s", prompt)
	}
	if !strings.Contains(prompt, "继续原来的意图") {
		t.Fatalf("continuation text lost:\n%s", prompt)
	}
}

// The archived copy goes through the same secret boundary as the journal: a
// credential in the prompt must not become durable in the brief.
func TestArchivedBriefRedactsKnownSecrets(t *testing.T) {
	const secret = "sk-super-secret-123456"
	s := &Server{CaseRoot: t.TempDir()}
	s.Secrets = func() []string { return []string{secret} }
	workDir := filepath.Join(s.CaseRoot, "in_1")
	rel, err := s.archiveBrief(workDir, "r_secret", "前 "+secret+" 后")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.CaseRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("archived brief kept a known secret: %s", data)
	}
	if !strings.Contains(string(data), "***") {
		t.Fatalf("archived brief was not redacted: %s", data)
	}
}

// Old run rows have no brief_path; new ones can be written and read back.
func TestRunBriefPathJSONCompatibility(t *testing.T) {
	var old model.Run
	if err := json.Unmarshal([]byte(`{"id":"r_old","status":"done"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.BriefPath != "" {
		t.Fatalf("old run grew a brief path: %q", old.BriefPath)
	}
	raw, err := json.Marshal(model.Run{ID: "r_plain"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "brief_path") {
		t.Fatalf("omitempty did not drop an empty path: %s", raw)
	}
	raw, err = json.Marshal(model.Run{ID: "r_new", BriefPath: "in_1/.saker/briefs/r_new.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"brief_path":"in_1/.saker/briefs/r_new.md"`) {
		t.Fatalf("brief_path missing: %s", raw)
	}
}
