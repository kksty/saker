package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardrailStore writes one operator cyber.yaml and loads it. TYPESAFE_API_KEY
// is cleared so a developer environment cannot change the jev_configured
// assertions.
func guardrailStore(t *testing.T, body string) *Store {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "cyber.yaml")
	const model = "llm:\n  base_url: http://gateway.test/v1\n  api_key: gw-key\n  model: route\n"
	if err := os.WriteFile(path, []byte(model+body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadAt(filepath.Join(dir, "settings.json"), path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestGuardrailPolicyNoneWhenSectionAbsent(t *testing.T) {
	st := guardrailStore(t, "")
	view := st.GuardrailPolicy()
	if view.Configured || view.Provider != "none" || view.Mode != "auto" || view.OnError != "" {
		t.Fatalf("view = %+v", view)
	}
	if view.Scope == nil || len(view.Scope) != 0 {
		t.Fatalf("scope = %+v", view.Scope)
	}
	if !strings.Contains(view.Reason, "未配置") {
		t.Fatalf("reason = %q", view.Reason)
	}
}

func TestGuardrailPolicyParsesJudgeConfiguration(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		want   GuardrailPolicyView
		absent []string
	}{
		"jev safe": {
			body: `extensions:
  guardrail:
    provider: jev
    mode: safe
    scope: [bash, subagent]
    allow_prefixes: ["git status", "ls"]
  jev:
    api_key: jev-secret
`,
			want: GuardrailPolicyView{
				Configured: true, Provider: "jev", Mode: "safe",
				Scope: []string{"bash", "subagent"}, AllowPrefixesCount: 2, JEVConfigured: true,
			},
		},
		"jev ignores unused zero-temperature llm block": {
			body: `guardrail:
  provider: jev
  llm:
    temperature: 0
jev:
  api_key: fixture
`,
			want: GuardrailPolicyView{
				Configured: true, Provider: "jev", Mode: "auto",
				Scope: []string{}, JEVConfigured: true,
			},
		},
		"llm judge": {
			body: `extensions:
  guardrail:
    provider: llm
    on_error: block
    llm:
      base_url: http://fixture:private-judge-url@judge.test/v1?token=private-judge-query
      model: judge-small
      api_key: judge-secret
`,
			want: GuardrailPolicyView{
				Configured: true, Provider: "llm", Mode: "auto", OnError: "block",
				Scope: []string{}, LLM: GuardrailLLMView{
					Configured: true, Model: "judge-small", BaseURL: "http://judge.test/v1",
				},
			},
		},
		"hybrid shadow allow": {
			body: `extensions:
  guardrail:
    provider: hybrid
    shadow: true
    on_error: allow
    llm:
      base_url: http://judge.test/v1
      model: judge-small
  jev:
    api_key: jev-secret
    base_url: https://judge.test/private-jev-prefix
`,
			want: GuardrailPolicyView{
				Configured: true, Provider: "hybrid", Mode: "auto", OnError: "allow", Shadow: true,
				Scope: []string{}, JEVConfigured: true, JEVCustomEndpoint: true,
				LLM: GuardrailLLMView{Configured: true, Model: "judge-small", BaseURL: "http://judge.test/v1"},
			},
		},
		"explicit none": {
			body: `extensions:
  guardrail:
    provider: none
`,
			want: GuardrailPolicyView{Configured: true, Provider: "none", Mode: "auto", Scope: []string{}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			view := guardrailStore(t, tc.body).GuardrailPolicy()
			got, _ := json.Marshal(view)
			want, _ := json.Marshal(tc.want)
			if string(got) != string(want) {
				t.Fatalf("policy =\n%s\nwant\n%s", got, want)
			}
			for _, secret := range []string{"jev-secret", "judge-secret", "private-jev-prefix", "private-judge-url", "private-judge-query"} {
				if strings.Contains(string(got), secret) {
					t.Fatalf("policy view leaked %q: %s", secret, got)
				}
			}
		})
	}
}

func TestGuardrailPolicyReportsInvalidConfiguration(t *testing.T) {
	// The fork rejects incomplete bindings and top-level on_error for JEV.
	// The console used to report these saved configurations as enabled.
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"missing provider": {`extensions:
  guardrail:
    mode: safe
`, "provider"},
		"unknown provider": {`extensions:
  guardrail:
    provider: magic
`, "不合法"},
		"bad mode": {`extensions:
  guardrail:
    provider: jev
    mode: aggressive
`, "mode"},
		"bad on_error": {`extensions:
  guardrail:
    provider: jev
    on_error: ignore
`, "on_error"},
		"bad shadow": {`extensions:
  guardrail:
    provider: jev
    shadow: sometimes
`, "shadow"},
		"bad scope": {`extensions:
  guardrail:
    provider: jev
    scope: bash
`, "scope"},
		"bad allow_prefixes": {`extensions:
  guardrail:
    provider: jev
    allow_prefixes: git
`, "allow_prefixes"},
		"bad llm": {`extensions:
  guardrail:
    provider: llm
    llm: yes
`, "llm"},
		"llm without binding": {`extensions:
  guardrail:
    provider: llm
`, "裁判无法生效"},
		"llm model only": {`guardrail:
  provider: llm
  llm:
    model: judge
`, "base_url"},
		"jev without credentials": {`guardrail:
  provider: jev
`, "缺少 JEV 凭据"},
		"jev with llm error policy": {`guardrail:
  provider: jev
  on_error: allow
jev:
  api_key: fixture
`, "仅适用于"},
		"jev with llm binding": {`guardrail:
  provider: jev
  llm:
    model: judge
jev:
  api_key: fixture
`, "guardrail.llm 仅在 provider=llm 或 hybrid 时生效"},
		"none with llm binding": {`guardrail:
  provider: none
  llm:
    base_url: http://judge.test/v1
`, "guardrail.llm 仅在 provider=llm 或 hybrid 时生效"},
		"jev with llm temperature": {`guardrail:
  provider: jev
  llm:
    temperature: 0.7
jev:
  api_key: fixture
`, "guardrail.llm 仅在 provider=llm 或 hybrid 时生效"},
		"jev with invalid endpoint": {`guardrail:
  provider: jev
jev:
  api_key: fixture
  base_url: https://judge.test/?token=private
`, "jev.base_url"},
		"unknown field": {`extensions:
  guardrail:
    provider: jev
    shadw: true
`, "未知字段"},
		"not a mapping": {`extensions:
  guardrail: jev
`, "映射"},
	} {
		t.Run(name, func(t *testing.T) {
			view := guardrailStore(t, tc.body).GuardrailPolicy()
			if view.Configured {
				t.Fatalf("invalid config reported as configured: %+v", view)
			}
			if !strings.Contains(view.Reason, tc.want) {
				t.Fatalf("reason = %q, want substring %q", view.Reason, tc.want)
			}
		})
	}
}

func TestGuardrailJEVKeyFromEnvironment(t *testing.T) {
	st := guardrailStore(t, `extensions:
  guardrail:
    provider: jev
`)
	t.Setenv("TYPESAFE_API_KEY", "env-jev-secret")
	view := st.GuardrailPolicy()
	if !view.Configured || !view.JEVConfigured {
		t.Fatalf("view = %+v", view)
	}
	body, _ := json.Marshal(view)
	if strings.Contains(string(body), "env-jev-secret") {
		t.Fatalf("environment secret leaked: %s", body)
	}
}

// The settings View is the console's one read: the guardrail summary must be in
// its JSON and no credential may ride along.
func TestSettingsViewJSONIncludesGuardrailPolicy(t *testing.T) {
	st := guardrailStore(t, `extensions:
  guardrail:
    provider: hybrid
    mode: safe
    shadow: true
    on_error: allow
    scope: [bash]
    llm:
      base_url: http://judge.test/v1
      model: judge-small
      api_key: judge-secret
  jev:
    api_key: jev-secret
`)
	body, err := json.Marshal(st.View())
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Guardrail struct {
			Configured         bool     `json:"configured"`
			Provider           string   `json:"provider"`
			Mode               string   `json:"mode"`
			OnError            string   `json:"on_error"`
			Shadow             bool     `json:"shadow"`
			Scope              []string `json:"scope"`
			AllowPrefixesCount int      `json:"allow_prefixes_count"`
			JEVConfigured      bool     `json:"jev_configured"`
			LLM                struct {
				Configured bool   `json:"configured"`
				Model      string `json:"model"`
				BaseURL    string `json:"base_url"`
			} `json:"llm"`
		} `json:"guardrail"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if !view.Guardrail.Configured || view.Guardrail.Provider != "hybrid" || view.Guardrail.Mode != "safe" ||
		!view.Guardrail.Shadow || view.Guardrail.OnError != "allow" || !view.Guardrail.JEVConfigured ||
		!view.Guardrail.LLM.Configured || view.Guardrail.LLM.Model != "judge-small" {
		t.Fatalf("guardrail view = %+v", view.Guardrail)
	}
	if len(view.Guardrail.Scope) != 1 || view.Guardrail.Scope[0] != "bash" {
		t.Fatalf("scope = %+v", view.Guardrail.Scope)
	}
	for _, secret := range []string{"judge-secret", "jev-secret", "gw-key"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("settings view leaked %q: %s", secret, body)
		}
	}
}
