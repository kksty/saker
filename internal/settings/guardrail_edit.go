package settings

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// GuardrailInput enables a judge or updates its connection. Empty connection
// fields keep their saved values, including write-only credentials.
type GuardrailInput struct {
	Provider   string `json:"provider"`
	LLMBaseURL string `json:"llm_base_url"`
	LLMModel   string `json:"llm_model"`
	LLMAPIKey  string `json:"llm_api_key"`
	JEVBaseURL string `json:"jev_base_url"`
	JEVAPIKey  string `json:"jev_api_key"`
}

var ErrGuardrailConfig = errors.New("守门策略配置无效")

// SaveGuardrail validates the candidate before publishing it. validate, when
// present, runs the scan binary's offline validator against the changed sections.
func (s *Store) SaveGuardrail(in GuardrailInput, validate func([]byte) error) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	root, doc, err := s.readDocument()
	if err != nil {
		return false, err
	}
	for _, section := range []string{"guardrail", "jev"} {
		if node := sectionNode(doc, section); node != nil && node.Kind != yaml.MappingNode {
			return false, fmt.Errorf("%w：%s 必须是映射", ErrGuardrailConfig, section)
		}
	}
	flattenLegacyExtensions(doc)
	guardrail := ensureMapping(doc, "guardrail")
	current := scalar(mapGet(guardrail, "provider"))
	if current != "" && current != "none" && current != in.Provider {
		return false, fmt.Errorf("%w：当前裁判已设置，请使用当前裁判更新连接", ErrGuardrailConfig)
	}
	usesLLM := in.Provider == "llm" || in.Provider == "hybrid"
	usesJEV := in.Provider == "jev" || in.Provider == "hybrid"
	if !usesLLM && !usesJEV {
		return false, fmt.Errorf("%w：请选择裁判", ErrGuardrailConfig)
	}
	for _, endpoint := range []struct{ field, value string }{
		{"guardrail.llm.base_url", in.LLMBaseURL}, {"jev.base_url", in.JEVBaseURL},
	} {
		value := strings.TrimSpace(endpoint.value)
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return false, fmt.Errorf("%w：%s 需要无凭据、查询参数或片段的 HTTP(S) 地址", ErrGuardrailConfig, endpoint.field)
		}
	}
	changed := current != in.Provider
	setScalar(guardrail, "provider", in.Provider)
	if usesLLM {
		if llm := mapGet(guardrail, "llm"); llm != nil && llm.Kind != yaml.MappingNode {
			return false, fmt.Errorf("%w：guardrail.llm 必须是映射", ErrGuardrailConfig)
		}
		changed = updateJudgeConnection(ensureMapping(guardrail, "llm"), in.LLMBaseURL, in.LLMModel, in.LLMAPIKey) || changed
	}
	if usesJEV && (strings.TrimSpace(in.JEVBaseURL) != "" || strings.TrimSpace(in.JEVAPIKey) != "") {
		changed = updateJudgeConnection(ensureMapping(doc, "jev"), in.JEVBaseURL, "", in.JEVAPIKey) || changed
	}
	if policy := guardrailPolicy(doc); !policy.Configured {
		return false, fmt.Errorf("%w：%s", ErrGuardrailConfig, policy.Reason)
	}
	if !changed {
		return false, nil
	}
	if validate != nil {
		sections := map[string]*yaml.Node{"guardrail": guardrail}
		if jev := mapGet(doc, "jev"); jev != nil {
			sections["jev"] = jev
		}
		candidate, err := yaml.Marshal(sections)
		if err != nil {
			return false, errors.New("无法编码守门策略候选配置")
		}
		if err := validate(candidate); err != nil {
			return false, fmt.Errorf("%w：%s", ErrGuardrailConfig, err)
		}
	}
	return true, s.writeHarnessDocument(root)
}

func updateJudgeConnection(node *yaml.Node, baseURL, model, apiKey string) bool {
	changed := false
	for _, field := range []struct{ key, value string }{{"base_url", baseURL}, {"model", model}, {"api_key", apiKey}} {
		value := strings.TrimSpace(field.value)
		if value != "" && value != scalar(mapGet(node, field.key)) {
			setScalar(node, field.key, value)
			changed = true
		}
	}
	return changed
}
