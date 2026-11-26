package decider

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// The documents are read per round from the provider, never cached here: it is
// the same read the executor brief uses. A copy taken at construction (the old
// Scope field) kept the commander on the old brief after the operator edited a
// document, while executors already ran under the new text.
func TestCommanderRoundReadsDocsLive(t *testing.T) {
	st := newStore(t)
	t.Cleanup(func() { _ = st.Close() })
	r := NewRunner(st, nil, "")
	r.ModelName = "fixture"
	var (
		mu   sync.Mutex
		docs = "## 起点\nscope-before\n\n"
	)
	r.Docs = func() string {
		mu.Lock()
		defer mu.Unlock()
		return docs
	}
	var prompts []string
	r.Commander = func(_ context.Context, prompt string, _ bool) (RoundResult, error) {
		prompts = append(prompts, prompt)
		return RoundResult{}, nil
	}
	if r.Run(context.Background(), "operator", true) == nil {
		t.Fatal("round did not run")
	}
	// The operator edits the brief: no runner setter, no restart, no settings
	// save — the next round must render the new text.
	mu.Lock()
	docs = "## 起点\nscope-after\n\n"
	mu.Unlock()
	if r.Run(context.Background(), "operator", true) == nil {
		t.Fatal("second round did not run")
	}
	if len(prompts) != 2 {
		t.Fatalf("rounds = %d, want 2", len(prompts))
	}
	if !strings.Contains(prompts[0], "scope-before") {
		t.Fatalf("first round does not carry the documents it was given:\n%s", prompts[0])
	}
	if !strings.Contains(prompts[1], "scope-after") {
		t.Fatalf("the round after the edit still carries the old documents:\n%s", prompts[1])
	}
	if strings.Contains(prompts[1], "scope-before") {
		t.Fatal("the round after the edit carries both versions: the old text must not survive anywhere")
	}
}

// A changed document text is also a different example of "the prompt reflects
// the store", but the provider is called once per round: reading it inside
// r.mu would deadlock against the store's own lock during a save.
func TestCommanderRoundReadsDocsOutsideTheRunnerLock(t *testing.T) {
	st := newStore(t)
	t.Cleanup(func() { _ = st.Close() })
	r := NewRunner(st, nil, "")
	r.ModelName = "fixture"
	r.Docs = func() string { return "## 起点\nlocked\n\n" }
	r.Commander = func(_ context.Context, _ string, _ bool) (RoundResult, error) {
		// The provider is called without r.mu held; a round that kept the lock
		// would block Status() here instead of returning.
		done := make(chan struct{})
		go func() { r.Status(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Status() blocked while a round was building its prompt: the runner lock is held across the documents read")
		}
		return RoundResult{}, nil
	}
	if r.Run(context.Background(), "operator", true) == nil {
		t.Fatal("round did not run")
	}
}
