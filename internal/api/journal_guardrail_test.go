package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/executor"
)

// TestRunEventWindowKeepsGuardrailKinds pins the read side of the P4-J audit
// events: the run event reader is a kind-agnostic JSONL tail, so
// kind=guardrail_decision / kind=guardrail_review rows reach the console
// unchanged (they are not filtered out like an unknown kind might be) and still
// decode into executor.Event - the shape web/src/lib/guardrail.ts renders.
//
// This is a read-path regression test only: no route, store or schema changed.
func TestRunEventWindowKeepsGuardrailKinds(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"event_id":"ev-1","turn_id":"turn-1","kind":"guardrail_decision","session_id":"session-1","at":"2026-11-18T04:00:00Z","guardrail_action":"block","guardrail_reason":"命令被策略阻断","guardrail_call_id":"call-1","guardrail_operation_id":"op-1","text":"guardrail block：命令被策略阻断"}`,
		`{"event_id":"ev-2","turn_id":"turn-1","kind":"guardrail_review","session_id":"session-1","at":"2026-11-18T04:00:01Z","guardrail_action":"review","guardrail_tool":"bash","guardrail_state":"pending","guardrail_call_id":"call-1","guardrail_operation_id":"op-1","guardrail_expires_at":"2026-11-18T04:05:00Z","guardrail_resolution_source":"control","text":"guardrail review bash：需要人工确认"}`,
	}
	path := filepath.Join(dataDir, "runs", "run-1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stampJournalVersion(t, path)

	events, total, err := readRunEventWindow(dataDir, "run-1", 200, false)
	if err != nil {
		t.Fatalf("read run journal: %v", err)
	}
	if total != 2 || len(events) != 2 {
		t.Fatalf("events = %d, total = %d, want 2/2", len(events), total)
	}

	var decision executor.Event
	if err := json.Unmarshal(events[0], &decision); err != nil {
		t.Fatalf("decision line is not executor.Event JSON: %v", err)
	}
	if decision.Kind != executor.KindGuardrailDecision || decision.GuardrailAction != "block" ||
		decision.GuardrailReason != "命令被策略阻断" || decision.GuardrailCallID != "call-1" ||
		decision.GuardrailOperationID != "op-1" {
		t.Fatalf("decision fields lost through the journal reader: %+v", decision)
	}

	var review executor.Event
	if err := json.Unmarshal(events[1], &review); err != nil {
		t.Fatalf("review line is not executor.Event JSON: %v", err)
	}
	if review.Kind != executor.KindGuardrailReview || review.GuardrailState != "pending" ||
		review.GuardrailTool != "bash" || !strings.Contains(review.Text, "bash") {
		t.Fatalf("review fields lost through the journal reader: %+v", review)
	}
	if review.GuardrailExpiresAt == nil || review.GuardrailResolutionSource != "control" {
		t.Fatalf("review deadline/resolution lost through the journal reader: %+v", review)
	}
}
