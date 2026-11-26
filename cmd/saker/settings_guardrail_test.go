package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/settings"
)

// settingsHubWith writes one operator cyber.yaml (model block + extra sections)
// and returns a hub that serves the instance settings API.
func settingsHubWith(t *testing.T, sections string) *hub {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	const model = "llm:\n  base_url: http://gateway.test/v1\n  api_key: gateway-key\n  model: route\n"
	if err := os.WriteFile(harness, []byte(model+sections), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := settings.LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatal(err)
	}
	return &hub{settings: st}
}

type settingsGuardrailJSON struct {
	Guardrail struct {
		Configured         bool     `json:"configured"`
		Provider           string   `json:"provider"`
		Mode               string   `json:"mode"`
		OnError            string   `json:"on_error"`
		Shadow             bool     `json:"shadow"`
		Scope              []string `json:"scope"`
		AllowPrefixesCount int      `json:"allow_prefixes_count"`
		JEVConfigured      bool     `json:"jev_configured"`
		Reason             string   `json:"reason"`
		LLM                struct {
			Configured bool   `json:"configured"`
			Model      string `json:"model"`
			BaseURL    string `json:"base_url"`
		} `json:"llm"`
	} `json:"guardrail"`
	APIKey string `json:"api_key"`
}

// The console's one settings read must carry the guardrail posture, and never a
// credential, for every operator configuration an instance can have.
func TestSettingsGETExposesGuardrailPolicy(t *testing.T) {
	for name, tc := range map[string]struct {
		sections string
		check    func(t *testing.T, got settingsGuardrailJSON)
	}{
		"none": {
			sections: "",
			check: func(t *testing.T, got settingsGuardrailJSON) {
				if got.Guardrail.Configured || got.Guardrail.Provider != "none" || got.Guardrail.Reason == "" {
					t.Fatalf("none = %+v", got.Guardrail)
				}
			},
		},
		"jev fail-open": {
			sections: `extensions:
  guardrail:
    provider: jev
    on_error: allow
  jev:
    api_key: jev-secret
`,
			check: func(t *testing.T, got settingsGuardrailJSON) {
				if !got.Guardrail.Configured || got.Guardrail.Provider != "jev" || got.Guardrail.OnError != "allow" || !got.Guardrail.JEVConfigured {
					t.Fatalf("jev = %+v", got.Guardrail)
				}
			},
		},
		"llm shadow": {
			sections: `extensions:
  guardrail:
    provider: llm
    shadow: true
    llm:
      base_url: http://judge.test/v1
      model: judge-small
      api_key: judge-secret
`,
			check: func(t *testing.T, got settingsGuardrailJSON) {
				if !got.Guardrail.Configured || !got.Guardrail.Shadow || !got.Guardrail.LLM.Configured || got.Guardrail.LLM.Model != "judge-small" {
					t.Fatalf("llm = %+v", got.Guardrail)
				}
			},
		},
		"hybrid safe scope": {
			sections: `extensions:
  guardrail:
    provider: hybrid
    mode: safe
    scope: [bash, subagent]
    allow_prefixes: ["git status"]
    llm:
      model: judge-small
`,
			check: func(t *testing.T, got settingsGuardrailJSON) {
				if !got.Guardrail.Configured || got.Guardrail.Mode != "safe" || got.Guardrail.AllowPrefixesCount != 1 ||
					len(got.Guardrail.Scope) != 2 || got.Guardrail.Scope[0] != "bash" {
					t.Fatalf("hybrid = %+v", got.Guardrail)
				}
			},
		},
		"invalid provider": {
			sections: `extensions:
  guardrail:
    provider: magic
`,
			check: func(t *testing.T, got settingsGuardrailJSON) {
				if got.Guardrail.Configured || got.Guardrail.Reason == "" {
					t.Fatalf("invalid = %+v", got.Guardrail)
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := settingsHubWith(t, tc.sections)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /api/settings = %d %s", rec.Code, rec.Body.String())
			}
			var got settingsGuardrailJSON
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.APIKey != "" {
				t.Fatalf("settings view echoed the model key: %q", got.APIKey)
			}
			for _, secret := range []string{"jev-secret", "judge-secret", "gateway-key"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatalf("settings view leaked %q: %s", secret, rec.Body.String())
				}
			}
			tc.check(t, got)
		})
	}
}
