package aopexec

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"

	pb "github.com/kksty/saker/internal/aop/pb"
)

// evalDetailExtensionURL is the harness evaluator's detail extension on status
// events (state eval_start / eval_end / eval_error). saker decodes the five
// declared fields by hand instead of linking the harness module just for this
// message: the wire format is stable and the harness validates the real proto on
// its side. An unknown or absent extension leaves the status event untouched,
// which is what an older harness without evaluation detail looks like.
const evalDetailExtensionURL = "type.googleapis.com/cyber.agent.EvalDetail"

// evalDetail is the subset of cyber.agent.EvalDetail the run journal records.
type evalDetail struct {
	Round     int
	MaxRounds int
	// Pass is nil when the event carries no verdict (eval_start / eval_error).
	// "Not judged" must not be recorded as "failed", so presence is tracked
	// instead of defaulting the bool to false.
	Pass   *bool
	Reason string
	Error  string
}

// findEvalDetail returns the evaluation detail carried by an event. A payload
// that cannot be decoded is reported as the detail's error: the event still
// reaches the journal and says why its detail is missing.
func findEvalDetail(ev *pb.Event) (evalDetail, bool) {
	for _, ext := range ev.GetExtensions() {
		if ext == nil || ext.GetTypeUrl() != evalDetailExtensionURL {
			continue
		}
		detail, err := decodeEvalDetail(ext.GetValue())
		if err != nil {
			return evalDetail{Error: "评估详情解码失败：" + err.Error()}, true
		}
		return detail, true
	}
	return evalDetail{}, false
}

// decodeEvalDetail parses cyber.agent.EvalDetail:
//
//	1 error (string), 2 max_rounds (uint32), 3 pass (bool),
//	4 reason (string), 5 round (uint32).
func decodeEvalDetail(raw []byte) (evalDetail, error) {
	var out evalDetail
	for len(raw) > 0 {
		num, typ, n := protowire.ConsumeTag(raw)
		if n < 0 {
			return out, protowire.ParseError(n)
		}
		raw = raw[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			value, n := protowire.ConsumeString(raw)
			if n < 0 {
				return out, protowire.ParseError(n)
			}
			out.Error, raw = value, raw[n:]
		case num == 2 && typ == protowire.VarintType:
			value, n := protowire.ConsumeVarint(raw)
			if n < 0 {
				return out, protowire.ParseError(n)
			}
			out.MaxRounds, raw = int(value), raw[n:]
		case num == 3 && typ == protowire.VarintType:
			value, n := protowire.ConsumeVarint(raw)
			if n < 0 {
				return out, protowire.ParseError(n)
			}
			pass := value != 0
			out.Pass, raw = &pass, raw[n:]
		case num == 4 && typ == protowire.BytesType:
			value, n := protowire.ConsumeString(raw)
			if n < 0 {
				return out, protowire.ParseError(n)
			}
			out.Reason, raw = value, raw[n:]
		case num == 5 && typ == protowire.VarintType:
			value, n := protowire.ConsumeVarint(raw)
			if n < 0 {
				return out, protowire.ParseError(n)
			}
			out.Round, raw = int(value), raw[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, raw)
			if n < 0 {
				return out, protowire.ParseError(n)
			}
			raw = raw[n:]
		}
	}
	return out, nil
}

// evalStatusText folds the evaluation detail into the status text. The run page
// already renders status text, so the verdict is visible without a front-end
// change; the structured fields stay on the event for later consumers.
func evalStatusText(state string, detail evalDetail) string {
	var b strings.Builder
	b.WriteString(state)
	if detail.Round > 0 || detail.MaxRounds > 0 {
		fmt.Fprintf(&b, " 第%d/%d轮", detail.Round, detail.MaxRounds)
	}
	if detail.Pass != nil {
		if *detail.Pass {
			b.WriteString("：通过")
		} else {
			b.WriteString("：未通过")
		}
	}
	if reason := strings.TrimSpace(detail.Reason); reason != "" {
		b.WriteString(" " + reason)
	}
	if failure := strings.TrimSpace(detail.Error); failure != "" {
		b.WriteString(" " + failure)
	}
	return b.String()
}
