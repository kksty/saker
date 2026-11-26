package aopexec

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/known/anypb"

	pb "github.com/kksty/saker/internal/aop/pb"
	"github.com/kksty/saker/internal/executor"
)

func boolp(v bool) *bool { return &v }

// encodeEvalDetail is the fixture side of the wire format: field numbers are
// pinned here on purpose, so a wrong decoder field number fails a test instead
// of silently recording the wrong round or verdict.
func encodeEvalDetail(detail evalDetail) []byte {
	var b []byte
	if detail.Error != "" {
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendString(b, detail.Error)
	}
	if detail.MaxRounds != 0 {
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(detail.MaxRounds))
	}
	if detail.Pass != nil {
		b = protowire.AppendTag(b, 3, protowire.VarintType)
		value := uint64(0)
		if *detail.Pass {
			value = 1
		}
		b = protowire.AppendVarint(b, value)
	}
	if detail.Reason != "" {
		b = protowire.AppendTag(b, 4, protowire.BytesType)
		b = protowire.AppendString(b, detail.Reason)
	}
	if detail.Round != 0 {
		b = protowire.AppendTag(b, 5, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(detail.Round))
	}
	return b
}

func evalEvent(state string, detail evalDetail) *pb.Event {
	return &pb.Event{
		Payload:    &pb.Event_Status{Status: &pb.Status{State: state}},
		Extensions: []*anypb.Any{{TypeUrl: evalDetailExtensionURL, Value: encodeEvalDetail(detail)}},
	}
}

// A passing eval_end carries round/max_rounds/pass/reason into the structured
// event fields and folds the verdict into the status text the run page already
// renders.
func TestEvalDetailPassingVerdictReachesStatusEvent(t *testing.T) {
	got := event(evalEvent("eval_end", evalDetail{
		Round: 2, MaxRounds: 3, Pass: boolp(true), Reason: "找到可复现 SQLi",
	}))
	if got.Kind != executor.KindStatus || got.State != "eval_end" {
		t.Fatalf("status event = %+v", got)
	}
	if got.EvalRound != 2 || got.EvalMaxRounds != 3 || got.EvalPass == nil || !*got.EvalPass {
		t.Fatalf("eval detail = %+v", got)
	}
	if got.EvalReason != "找到可复现 SQLi" || got.EvalError != "" {
		t.Fatalf("eval reason/error = %q/%q", got.EvalReason, got.EvalError)
	}
	for _, want := range []string{"eval_end", "第2/3轮", "通过", "找到可复现 SQLi"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("status text %q lacks %q", got.Text, want)
		}
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"eval_pass":true`, `"eval_round":2`, `"eval_max_rounds":3`, `"eval_reason":"找到可复现 SQLi"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("journal line %s lacks %s", encoded, want)
		}
	}
}

// eval_start carries progress but no verdict: pass must stay absent, not false,
// and the text must not claim the goal failed.
func TestEvalDetailStartHasNoVerdict(t *testing.T) {
	got := event(evalEvent("eval_start", evalDetail{Round: 1, MaxRounds: 3}))
	if got.EvalRound != 1 || got.EvalMaxRounds != 3 {
		t.Fatalf("eval detail = %+v", got)
	}
	if got.EvalPass != nil {
		t.Fatalf("a start event gained a verdict: %v", *got.EvalPass)
	}
	if strings.Contains(got.Text, "通过") || !strings.Contains(got.Text, "第1/3轮") {
		t.Fatalf("start text = %q", got.Text)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "eval_pass") {
		t.Fatalf("start event wrote a zero verdict: %s", encoded)
	}
}

// An eval_error carries the evaluator failure, no verdict, and the reason the
// round could not be judged is visible in the status text.
func TestEvalDetailErrorReachesStatusEvent(t *testing.T) {
	got := event(evalEvent("eval_error", evalDetail{Round: 2, MaxRounds: 3, Error: "provider timeout"}))
	if got.EvalPass != nil {
		t.Fatalf("an error event gained a verdict: %v", *got.EvalPass)
	}
	if got.EvalError != "provider timeout" || got.EvalReason != "" {
		t.Fatalf("eval error = %q", got.EvalError)
	}
	if !strings.Contains(got.Text, "eval_error") || !strings.Contains(got.Text, "provider timeout") {
		t.Fatalf("status text = %q", got.Text)
	}
}

// A failing verdict keeps pass=false explicitly (true presence, false value) so
// "judged and failed" stays distinguishable from "not judged".
func TestEvalDetailFailingVerdictIsExplicitFalse(t *testing.T) {
	got := event(evalEvent("eval_end", evalDetail{Round: 3, MaxRounds: 3, Pass: boolp(false), Reason: "端口 443 未扫描"}))
	if got.EvalPass == nil || *got.EvalPass {
		t.Fatalf("fail verdict = %v", got.EvalPass)
	}
	if !strings.Contains(got.Text, "未通过") || !strings.Contains(got.Text, "端口 443 未扫描") {
		t.Fatalf("status text = %q", got.Text)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"eval_pass":false`) {
		t.Fatalf("failed verdict was omitted: %s", encoded)
	}
}

// Unknown fields are skipped (the harness message may grow) and an event with
// no eval extension keeps the old plain status text.
func TestEvalDetailSkipsUnknownFieldsAndAbsentExtension(t *testing.T) {
	raw := encodeEvalDetail(evalDetail{Round: 1, MaxRounds: 2, Pass: boolp(true)})
	raw = protowire.AppendTag(raw, 99, protowire.VarintType)
	raw = protowire.AppendVarint(raw, 7)
	detail, err := decodeEvalDetail(raw)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Round != 1 || detail.MaxRounds != 2 || detail.Pass == nil || !*detail.Pass {
		t.Fatalf("decoded = %+v", detail)
	}

	plain := event(&pb.Event{Payload: &pb.Event_Status{Status: &pb.Status{State: "compact_start"}}})
	if plain.Text != "compact_start" || plain.EvalPass != nil || plain.EvalRound != 0 {
		t.Fatalf("plain status = %+v", plain)
	}
	if _, ok := findEvalDetail(&pb.Event{}); ok {
		t.Fatal("absent extension reported as present")
	}
	if _, err := decodeEvalDetail([]byte{0xff}); err == nil {
		t.Fatal("malformed detail decoded without an error")
	}
}
