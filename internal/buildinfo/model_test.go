package buildinfo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/testutil"
)

const testAPIKey = "sk-test-abcdef0123456789"

// The probe exists to record the real upstream model id. A gateway that echoes
// the aggregate alias in the body but reports the upstream model in a header must
// have both recorded, and the request must be the documented minimal one.
func TestProbeRecordsReturnedModelAndUpstreamAttribution(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	server := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Magpie-Model", "opencode-go/deepseek-v4.1-flash")
		w.Header().Set("X-Magpie-Provider", "opencode-go")
		_, _ = io.WriteString(w, `{"model":"group/smart-split","choices":[{"finish_reason":"length","message":{"content":""}}]}`)
	}))
	defer server.Close()

	probe := runModelProbe(context.Background(), server.URL, "openai", "group/smart-split", testAPIKey,
		[]string{testAPIKey}, 5*time.Second)
	if !probe.Probed || !probe.Available {
		t.Fatalf("probe = %+v", probe)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer "+testAPIKey {
		t.Fatalf("authorization header = %q", gotAuth)
	}
	if gotBody["max_tokens"] != float64(1) {
		t.Fatalf("max_tokens = %v", gotBody["max_tokens"])
	}
	if probe.ReturnedModel == nil || *probe.ReturnedModel != "group/smart-split" {
		t.Fatalf("returned model = %v", probe.ReturnedModel)
	}
	if probe.UpstreamModel == nil || *probe.UpstreamModel != "opencode-go/deepseek-v4.1-flash" {
		t.Fatalf("upstream model = %v", probe.UpstreamModel)
	}
	if probe.UpstreamProvider == nil || *probe.UpstreamProvider != "opencode-go" {
		t.Fatalf("upstream provider = %v", probe.UpstreamProvider)
	}
	// The one thing the manifest must never contain: the key itself.
	encoded, err := json.Marshal(probe)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), testAPIKey) {
		t.Fatalf("probe JSON leaked the API key: %s", encoded)
	}
}

func TestProbeRedactsAPIKeyFromErrors(t *testing.T) {
	server := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key `+testAPIKey+`"}}`)
	}))
	defer server.Close()

	probe := runModelProbe(context.Background(), server.URL, "openai", "m", testAPIKey, []string{testAPIKey}, 5*time.Second)
	if probe.Probed {
		if probe.Available {
			t.Fatalf("401 reported available: %+v", probe)
		}
		if probe.Error == nil {
			t.Fatal("401 recorded no error")
		}
		if strings.Contains(*probe.Error, testAPIKey) {
			t.Fatalf("error leaked the API key: %q", *probe.Error)
		}
		if probe.HTTPStatus == nil || *probe.HTTPStatus != http.StatusUnauthorized {
			t.Fatalf("http status = %v", probe.HTTPStatus)
		}
	}
}

func TestProbeAnthropicUsesMessagesEnvelope(t *testing.T) {
	var gotPath, gotVersion, gotKey string
	server := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotVersion = r.Header.Get("anthropic-version")
		gotKey = r.Header.Get("x-api-key")
		_, _ = io.WriteString(w, `{"model":"claude-real","stop_reason":"max_tokens","content":[]}`)
	}))
	defer server.Close()

	probe := runModelProbe(context.Background(), server.URL, "anthropic", "claude-configured", testAPIKey, []string{testAPIKey}, 5*time.Second)
	if !probe.Available || probe.ReturnedModel == nil || *probe.ReturnedModel != "claude-real" {
		t.Fatalf("probe = %+v", probe)
	}
	if gotPath != "/v1/messages" || gotVersion == "" || gotKey != testAPIKey {
		t.Fatalf("path=%q version=%q", gotPath, gotVersion)
	}
}

func TestProbeSkipsWithoutConfiguration(t *testing.T) {
	cases := []struct {
		name  string
		base  string
		model string
		key   string
	}{
		{"no base url", "", "m", "k"},
		{"no model", "http://127.0.0.1:1", "", "k"},
		{"no key", "http://127.0.0.1:1", "m", ""},
	}
	for _, tc := range cases {
		probe := runModelProbe(context.Background(), tc.base, "openai", tc.model, tc.key, nil, time.Second)
		if probe.Probed || probe.Reason == nil || *probe.Reason == "" {
			t.Fatalf("%s: probe = %+v", tc.name, probe)
		}
	}
}

// The manifest reads the same active profile the harness does, and records only
// the URL's scheme+host — never userinfo or query.
func TestCollectModelFromHarnessConfig(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "cyber.yaml")
	body := `llm:
    active_profile: chosen
    providers:
        - id: other
          base_url: http://other.test/v1
          api_key: other-key
          model: other-model
        - id: chosen
          provider: openai
          base_url: http://user:` + testAPIKey + `@gateway.test:8080/v1?trace=1
          api_key: ` + testAPIKey + `
          model: group/smart-split
`
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	mi := collectModel(context.Background(), root, Options{Root: root, HarnessConfig: config})
	if !mi.ConfigPresent {
		t.Fatal("config not present")
	}
	if mi.ActiveProfile == nil || *mi.ActiveProfile != "chosen" {
		t.Fatalf("active profile = %v", mi.ActiveProfile)
	}
	if mi.ModelID == nil || *mi.ModelID != "group/smart-split" {
		t.Fatalf("model = %v", mi.ModelID)
	}
	if mi.Provider == nil || *mi.Provider != "openai" {
		t.Fatalf("provider = %v", mi.Provider)
	}
	if mi.BaseURL == nil || *mi.BaseURL != "http://gateway.test:8080" {
		t.Fatalf("base url = %v", mi.BaseURL)
	}
	if mi.BaseURLPath == nil || *mi.BaseURLPath != "/v1" {
		t.Fatalf("base url path = %v", mi.BaseURLPath)
	}
	if !mi.APIKeyPresent {
		t.Fatal("api_key_present = false")
	}
	encoded, err := json.Marshal(mi)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), testAPIKey) || strings.Contains(string(encoded), "user:") {
		t.Fatalf("model info leaked credentials: %s", encoded)
	}
}

func TestCollectModelWithoutConfigRecordsReason(t *testing.T) {
	root := t.TempDir()
	mi := collectModel(context.Background(), root, Options{
		Root:          root,
		HarnessConfig: filepath.Join(root, "absent.yaml"),
		ProbeModel:    true,
	})
	if mi.ConfigPresent {
		t.Fatal("absent config reported present")
	}
	if mi.Probe == nil || mi.Probe.Probed || mi.Probe.Reason == nil || *mi.Probe.Reason != "no_harness_config" {
		t.Fatalf("probe = %+v", mi.Probe)
	}
}
