package api

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
)

// Gateway failures are reported from provider error events. Authentication and
// quota rejection pause project work; external gateways own key rotation and
// retry policy. Model messages and tool results never drive this classifier.
type providerFailure int

const (
	providerFailureNone providerFailure = iota
	// providerFailureAuth: the provider rejected the credential (401/403,
	// invalid key, unauthorized).
	providerFailureAuth
	// providerFailureQuota: the credential is valid but out of quota/balance.
	providerFailureQuota
	// providerFailureRate: throttled (429, rate limit, overloaded). The key is
	// good; rotating would burn a valid credential for nothing.
	providerFailureRate
	// providerFailureTransient: anything else that failed the turn (network,
	// 5xx, harness error). Retrying may help; rotating would not.
	providerFailureTransient
)

func (f providerFailure) String() string {
	switch f {
	case providerFailureAuth:
		return "auth"
	case providerFailureQuota:
		return "quota"
	case providerFailureRate:
		return "rate"
	case providerFailureTransient:
		return "transient"
	default:
		return "none"
	}
}

// requiresOperator identifies terminal gateway rejection.
func (f providerFailure) requiresOperator() bool {
	return f == providerFailureAuth || f == providerFailureQuota
}

// providerFailureRule is one table row. Order is fixed: exact provider codes
// before fuzzy words, auth before quota before rate.
type providerFailureRule struct {
	class   providerFailure
	needles []string
}

// providerFailureRules is the single table the classifier reads. Needles are
// lowercase; the harness format is `API error (<NNN>): <message>` and vendor
// bodies use the code names below.
var providerFailureRules = []providerFailureRule{
	{providerFailureAuth, []string{
		"api error (401)", "api error (403)",
		"invalid_api_key", "invalid api key", "incorrect api key",
		"unauthorized", "authentication", "authentication_error",
		"permission denied", "invalid token",
	}},
	{providerFailureQuota, []string{
		"api error (402)",
		"insufficient_quota", "insufficient quota",
		"insufficient_balance", "insufficient balance",
		"quota exceeded", "quota_exceeded", "billing", "credit balance",
		"payment required", "余额", "欠费",
		"额度已用尽", "额度用尽", "额度不足", "配额不足", "配额已用完",
	}},
	{providerFailureRate, []string{
		"api error (429)", "rate limit", "rate_limit", "too many requests",
		"overloaded", "overloaded_error",
	}},
}

// classifyProviderFailure turns one failed run's structured result into a
// credential class. Success is not classified. The text it reads is only the
// provider contract (see the file comment); callers pass a Result whose Output
// may have the journal's error-event texts appended.
func classifyProviderFailure(res executor.Result) providerFailure {
	if !res.IsError && !strings.EqualFold(strings.TrimSpace(res.StopReason), "error") {
		return providerFailureNone
	}
	text := strings.ToLower(providerFailureText(res))
	for _, rule := range providerFailureRules {
		for _, needle := range rule.needles {
			if strings.Contains(text, needle) {
				return rule.class
			}
		}
	}
	return providerFailureTransient
}

// providerFailureText is the exact text the classifier may read: the
// transport's stderr/Output plus the harness's stop reason. Never model
// messages, never tool output.
func providerFailureText(res executor.Result) string {
	return res.Output + "\n" + res.StopReason
}

// providerErrorEvidence reads the run's journal and returns the text of its
// kind=error events only. Provider failures show up there as
// `API error (401): …`; the tool/message events around them are context for a
// human, not a credential signal, so they are ignored.
func (s *Server) providerErrorEvidence(runID string) string {
	if strings.TrimSpace(s.DataDir) == "" || !isPlainID(runID) {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(s.DataDir, "runs", runID+".jsonl"))
	if err != nil || len(data) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	// A provider failure is at the end of the turn; the head is thousands of
	// ordinary events.
	if len(lines) > 400 {
		lines = lines[len(lines)-400:]
	}
	var b strings.Builder
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, `"kind":"`+executor.KindError+`"`) {
			continue
		}
		var ev struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Kind != executor.KindError || ev.Text == "" {
			continue
		}
		b.WriteString(ev.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// afterProviderFailure reports terminal gateway rejection; credentials and
// retries belong to the external gateway, never to task orchestration.
func (s *Server) afterProviderFailure(runID, intentID, status string, res executor.Result) {
	if status != model.RunFailed {
		return
	}
	if class := s.providerFailureClass(runID, res); class.requiresOperator() {
		s.pauseForProviderFailure(class, "模型网关拒绝请求")
	}
}

// providerFailureClass merges the transport result with the journal's structured
// error events and classifies it. Both the executor hook and the commander round
// use this one function, so "is this the provider saying no?" has one answer.
func (s *Server) providerFailureClass(runID string, res executor.Result) providerFailure {
	probe := res
	if evidence := s.providerErrorEvidence(runID); evidence != "" {
		probe.Output = probe.Output + "\n" + evidence
	}
	return classifyProviderFailure(probe)
}

// pauseForProviderFailure is D67 wired. A credential that is gone is not a
// transient error: retrying it on a heartbeat only burns rounds and leaves
// half-finished cases. Stop dispatch, pause the commander and cancel live runs
// (their intents settle back to pending); only POST /api/project/resume brings
// the project back, and a human decides when.
func (s *Server) pauseForProviderFailure(class providerFailure, why string) {
	if s.paused.Load() {
		return
	}
	s.SetPaused(true)
	reason := fmt.Sprintf("模型网关不可用（%s，%s）：检查外部模型网关后手动继续", class, why)
	if s.Decider != nil {
		s.Decider.PauseFor(reason)
	}
	canceled := s.CancelAllWithCause(model.CancelCausePause, reason)
	log.Printf("provider_event=project_paused class=%s why=%q canceled=%d", class, why, canceled)
}

// addProviderWarning appends one operator-facing warning to a run. Warnings are
// diagnostics, not facts: they never change a run's status or an intent's state.
// The append itself lives in addRunWarning so every warning source shares one
// dedupe rule.
func (s *Server) addProviderWarning(runID, text string) {
	s.addRunWarning(runID, text)
}
