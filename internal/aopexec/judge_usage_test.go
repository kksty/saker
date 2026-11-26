package aopexec

import (
	"encoding/json"
	"strings"
	"testing"

	pb "github.com/kksty/saker/internal/aop/pb"
	"github.com/kksty/saker/internal/executor"
)

// judgeUsageEvent is the AOP event the harness guardrail runtime publishes for
// one of its own model calls: Emitter=guardrail (JEV) or guardrail-llm, payload
// Event_Usage. Its tokens are the judge's, not the judged run's.
func judgeUsageEvent(emitter string, total, in, out int) *pb.Event {
	return &pb.Event{
		Id:        "ev-judge-" + emitter,
		SessionId: "session-1",
		TurnId:    "turn-1",
		Emitter:   emitter,
		Payload: &pb.Event_Usage{Usage: &pb.TokenUsage{
			TotalTokens:  uint64(total),
			InputTokens:  uint64(in),
			OutputTokens: uint64(out),
			Detail:       map[string]uint64{"context_tokens": 999999},
		}},
	}
}

// TestUsageEmitterSplitIsThreeWay pins the classification: run usage keeps the
// old path, guardrail usage is JEV, guardrail-llm usage is the LLM judge. The
// test walks all three through the same mapping and absorb functions the real
// stream uses, and reads the journal shape back from JSON.
func TestUsageEmitterSplitIsThreeWay(t *testing.T) {
	tests := []struct {
		name    string
		emitter string
		// kind/judge of the mapped event.
		wantKind  string
		wantJudge string
		// judge buckets after absorb.
		wantJEV, wantLLM executor.JudgeUsage
		// run totals after absorb.
		wantRunTokens int
	}{
		{
			name: "run own usage", emitter: "",
			wantKind: executor.KindUsage, wantRunTokens: 30,
		},
		{
			name: "legacy/unclassified emitter stays run usage", emitter: "harness",
			wantKind: executor.KindUsage, wantRunTokens: 30,
		},
		{
			name: "JEV judge", emitter: executor.EmitterGuardrail,
			wantKind: executor.KindJudgeUsage, wantJudge: executor.JudgeJEV,
			wantJEV: executor.JudgeUsage{Tokens: 30, InputTokens: 20, OutputTokens: 10, Calls: 1},
		},
		{
			name: "LLM judge", emitter: executor.EmitterGuardrailLLM,
			wantKind: executor.KindJudgeUsage, wantJudge: executor.JudgeLLM,
			wantLLM: executor.JudgeUsage{Tokens: 30, InputTokens: 20, OutputTokens: 10, Calls: 1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ev := event(judgeUsageEvent(test.emitter, 30, 20, 10))
			if ev.Kind != test.wantKind {
				t.Fatalf("kind = %q, want %q: %+v", ev.Kind, test.wantKind, ev)
			}
			if ev.Emitter != test.emitter {
				t.Fatalf("emitter = %q, want %q", ev.Emitter, test.emitter)
			}
			if ev.Judge != test.wantJudge {
				t.Fatalf("judge = %q, want %q", ev.Judge, test.wantJudge)
			}
			if ev.TurnID != "turn-1" || ev.SessionID != "session-1" {
				t.Fatalf("event identity lost: %+v", ev)
			}

			var res executor.Result
			absorb(&res, ev)
			if res.Tokens != test.wantRunTokens || res.InputTokens != test.wantRunTokens/3*2 || res.OutputTokens != test.wantRunTokens/3 {
				t.Fatalf("run usage = %+v, want tokens %d", res, test.wantRunTokens)
			}
			if res.JudgeJEV != test.wantJEV || res.JudgeLLM != test.wantLLM {
				t.Fatalf("judge usage = JEV %+v LLM %+v", res.JudgeJEV, res.JudgeLLM)
			}
			// A judge's context size must never become the run's context pressure.
			if ev.Kind == executor.KindJudgeUsage && (res.ContextTokens != 0 || res.ContextWindow != 0) {
				t.Fatalf("judge context leaked into the run: %+v", res)
			}
		})
	}
}

// TestJudgeUsageJournalShape: kind=judge_usage carries judge, the documented
// input/output/total names, the emitter and the session/turn identity — the
// fields the project breakdown scans for. A run's own usage line keeps kind=usage
// and carries none of the judge vocabulary.
func TestJudgeUsageJournalShape(t *testing.T) {
	jev := event(judgeUsageEvent(executor.EmitterGuardrail, 30, 20, 10))
	encoded, err := json.Marshal(jev)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"kind":"judge_usage"`,
		`"judge":"jev"`,
		`"input":20`,
		`"output":10`,
		`"total":30`,
		`"emitter":"guardrail"`,
		`"session_id":"session-1"`,
		`"turn_id":"turn-1"`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("judge journal line missing %s: %s", want, encoded)
		}
	}
	// The run's own usage keeps the old shape: no judge keys at all.
	runUsage := event(&pb.Event{Emitter: "", Payload: &pb.Event_Usage{Usage: &pb.TokenUsage{TotalTokens: 30, InputTokens: 20, OutputTokens: 10}}})
	encoded, err = json.Marshal(runUsage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"kind":"usage"`) || !strings.Contains(string(encoded), `"tokens":30`) {
		t.Fatalf("run usage lost its shape: %s", encoded)
	}
	for _, unwanted := range []string{`"judge":`, `"input":`, `"output":`, `"total":`} {
		if strings.Contains(string(encoded), unwanted) {
			t.Fatalf("run usage line carries judge vocabulary %s: %s", unwanted, encoded)
		}
	}
}

// TestJudgeUsageAccumulatesWithoutTouchingTheRun: a turn is a mix of run calls
// and judge calls. The judge bucket is a per-call sum; the run keeps exactly the
// run's own numbers, and the authoritative turn_ended total is still the run's.
func TestJudgeUsageAccumulatesWithoutTouchingTheRun(t *testing.T) {
	var res executor.Result
	absorb(&res, event(judgeUsageEvent("", 100, 80, 20)))
	absorb(&res, event(judgeUsageEvent(executor.EmitterGuardrail, 30, 20, 10)))
	absorb(&res, event(judgeUsageEvent(executor.EmitterGuardrail, 12, 8, 4)))
	absorb(&res, event(judgeUsageEvent(executor.EmitterGuardrailLLM, 6, 4, 2)))

	if res.Tokens != 100 || res.InputTokens != 80 || res.OutputTokens != 20 {
		t.Fatalf("run totals were polluted by judge usage: %+v", res)
	}
	if res.JudgeJEV != (executor.JudgeUsage{Tokens: 42, InputTokens: 28, OutputTokens: 14, Calls: 2}) {
		t.Fatalf("JEV = %+v", res.JudgeJEV)
	}
	if res.JudgeLLM != (executor.JudgeUsage{Tokens: 6, InputTokens: 4, OutputTokens: 2, Calls: 1}) {
		t.Fatalf("LLM = %+v", res.JudgeLLM)
	}
	// The run's own turn_ended total is authoritative and does not move.
	absorb(&res, executor.Event{Kind: executor.KindTurnEnd, OK: executor.BoolPtr(true), StopReason: "completed", Tokens: 100, InputTokens: 80, OutputTokens: 20, UsageRequests: 1})
	if res.Tokens != 100 || res.InputTokens != 80 || res.JudgeJEV.Tokens != 42 || res.JudgeLLM.Tokens != 6 {
		t.Fatalf("turn_ended disturbed the split: %+v", res)
	}
}
