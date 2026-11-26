package settings

// Guardrail policy summary.
//
// The console has to be able to say whether tool admission is actually in
// effect, whether a judge failure fails open, and whether a shadow rollout is
// silently allowing everything. That answer lives in the operator's cyber.yaml
// (the flat `guardrail` and `jev` sections), which the control plane never
// edits: this file only reads it.
//
// The shape follows the harness fork's pluggable-judge configuration:
//
//	guardrail:
//	  provider: none|jev|llm|hybrid
//	  mode: auto|safe
//	  on_error: allow|review|block        # default review
//	  shadow: false                       # judge+record, always allow
//	  scope: [bash, subagent]             # empty = all tools
//	  allow_prefixes: ["git status"]      # safe mode only
//	  llm: { base_url, model, api_key, temperature, max_tokens }
//	jev:
//	  api_key: ...                        # or TYPESAFE_API_KEY
//
// A missing section is "not configured" and a malformed one is reported as
// configured=false with a short reason; neither may break the rest of the
// settings page. Secrets are never copied into the view.

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// GuardrailPolicyView is the read-only guardrail summary returned inside the
// instance settings view. Configured is false when the `guardrail`
// section is missing, has no provider, or would be rejected by the harness; Reason then
// says why (one sentence, no secrets).
type GuardrailPolicyView struct {
	Configured bool   `json:"configured"`
	Provider   string `json:"provider"` // none|jev|llm|hybrid
	Mode       string `json:"mode"`     // auto|safe
	OnError    string `json:"on_error"` // allow|review|block
	// Shadow means the judge only observes: every intercepted call is allowed
	// and recorded. A console must not render that as "passed review".
	Shadow bool `json:"shadow"`
	// Scope is the tool-name allowlist the policy applies to; empty = all tools.
	Scope []string `json:"scope"`
	// AllowPrefixesCount counts safe-mode prefixes that bypass review. Only the
	// count is exposed: the prefixes themselves can describe target commands.
	AllowPrefixesCount int              `json:"allow_prefixes_count"`
	JEVConfigured      bool             `json:"jev_configured"`
	LLM                GuardrailLLMView `json:"llm"`
	Reason             string           `json:"reason,omitempty"`
}

// GuardrailLLMView is the judge model binding without its credential. A
// dedicated llm sub-section is needed for provider=llm/hybrid to be usable; an
// empty one means the judge has no dedicated connection configured.
type GuardrailLLMView struct {
	Configured bool   `json:"configured"`
	Model      string `json:"model"`
	BaseURL    string `json:"base_url"`
}

// guardrailSectionKeys are the fields the harness fork's guardrail Section
// declares. An unknown field here would be rejected by the scan's strict
// decoder ("extension guardrail: ..."), so the summary reports it instead of
// claiming the policy is in effect.
var guardrailSectionKeys = map[string]bool{
	"provider": true, "mode": true, "on_error": true, "shadow": true,
	"scope": true, "allow_prefixes": true, "timeout": true, "concurrency": true,
	"review_timeout": true, "jev": true, "llm": true,
}

// GuardrailPolicy reads the operator's guardrail configuration. It is a pure
// read: no file is written, no host is contacted, and every credential stays
// out of the returned value.
func (s *Store) GuardrailPolicy() GuardrailPolicyView {
	view := GuardrailPolicyView{
		Provider: "none", Mode: "auto", OnError: "review", Scope: []string{},
		JEVConfigured: jevKeyConfigured(nil),
	}
	_, doc, err := s.readDocument()
	if err != nil {
		view.Reason = "无法读取 harness 配置，守门状态未知"
		return view
	}
	guardrailNode := sectionNode(doc, "guardrail")
	view.JEVConfigured = jevKeyConfigured(sectionNode(doc, "jev"))
	if guardrailNode == nil {
		view.Reason = "未配置 guardrail：工具审批未启用（默认 provider=none）"
		return view
	}
	if guardrailNode.Kind != yaml.MappingNode {
		view.Reason = "guardrail 必须是映射"
		return view
	}
	for i := 0; i+1 < len(guardrailNode.Content); i += 2 {
		key := guardrailNode.Content[i].Value
		if !guardrailSectionKeys[key] {
			view.Reason = fmt.Sprintf("guardrail 含未知字段 %q，宿主会拒绝该配置", key)
			return view
		}
	}
	provider := strings.ToLower(scalar(mapGet(guardrailNode, "provider")))
	switch provider {
	case "":
		view.Reason = "guardrail.provider 为空（默认 none）：工具审批未启用"
		return view
	case "none", "jev", "llm", "hybrid":
		view.Provider = provider
	default:
		view.Reason = fmt.Sprintf("guardrail.provider=%q 不合法（只允许 none/jev/llm/hybrid）", provider)
		return view
	}
	mode := strings.ToLower(scalar(mapGet(guardrailNode, "mode")))
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "safe" {
		view.Reason = fmt.Sprintf("guardrail.mode=%q 不合法（只允许 auto/safe）", mode)
		return view
	}
	view.Mode = mode
	onError := strings.ToLower(scalar(mapGet(guardrailNode, "on_error")))
	if onError == "" {
		onError = "review"
	}
	if onError != "allow" && onError != "review" && onError != "block" {
		view.Reason = fmt.Sprintf("guardrail.on_error=%q 不合法（只允许 allow/review/block）", onError)
		return view
	}
	view.OnError = onError
	if shadow := mapGet(guardrailNode, "shadow"); shadow != nil {
		value, err := boolScalar(shadow)
		if err != nil {
			view.Reason = "guardrail.shadow 不是布尔值"
			return view
		}
		view.Shadow = value
	}
	if scope := mapGet(guardrailNode, "scope"); scope != nil {
		tools, err := stringList(scope)
		if err != nil {
			view.Reason = "guardrail.scope 必须是工具名列表"
			return view
		}
		if tools != nil {
			view.Scope = tools
		}
	}
	if prefixes := mapGet(guardrailNode, "allow_prefixes"); prefixes != nil {
		items, err := stringList(prefixes)
		if err != nil {
			view.Reason = "guardrail.allow_prefixes 必须是字符串列表"
			return view
		}
		view.AllowPrefixesCount = len(items)
	}
	if llm := mapGet(guardrailNode, "llm"); llm != nil {
		if llm.Kind != yaml.MappingNode {
			view.Reason = "guardrail.llm 必须是映射"
			return view
		}
		view.LLM = GuardrailLLMView{
			Model:   scalar(mapGet(llm, "model")),
			BaseURL: scalar(mapGet(llm, "base_url")),
		}
		view.LLM.Configured = view.LLM.Model != "" || view.LLM.BaseURL != ""
	}
	// A judge provider with no dedicated llm binding is still "configured" for
	// the jev path; for llm/hybrid the console needs to know it cannot run.
	if (view.Provider == "llm" || view.Provider == "hybrid") && !view.LLM.Configured {
		view.Reason = fmt.Sprintf("provider=%s 但 guardrail.llm 未配置 base_url/model：裁判无法生效", view.Provider)
		return view
	}
	view.Configured = true
	return view
}

// jevKeyConfigured reports whether the JEV credential exists in the operator
// file or in the process environment (the harness reads TYPESAFE_API_KEY as a
// fallback). The value itself is never returned.
func jevKeyConfigured(jev *yaml.Node) bool {
	if strings.TrimSpace(scalar(mapGet(jev, "api_key"))) != "" {
		return true
	}
	return strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")) != ""
}

func boolScalar(node *yaml.Node) (bool, error) {
	if node.Kind != yaml.ScalarNode {
		return false, fmt.Errorf("not a scalar")
	}
	value := strings.ToLower(strings.TrimSpace(node.Value))
	switch value {
	case "true", "yes", "on":
		return true, nil
	case "false", "no", "off", "":
		return false, nil
	}
	if parsed, err := strconv.ParseBool(value); err == nil {
		return parsed, nil
	}
	return false, fmt.Errorf("not a bool")
}

func stringList(node *yaml.Node) ([]string, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("not a list")
	}
	out := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("not a string")
		}
		out = append(out, strings.TrimSpace(item.Value))
	}
	return out, nil
}
