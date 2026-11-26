package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kksty/saker/internal/testutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectivityUsesInferenceOnlyWhenRequested(t *testing.T) {
	posts, gets := 0, 0
	gateway := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			gets++
			http.NotFound(w, r)
			return
		}
		posts++
		if r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("wrong credential")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "requested" || body["response_format"] != nil || body["tools"] != nil {
			t.Errorf("unexpected probe request: %v", body)
		}
		fmt.Fprint(w, `{"model":"served-alias","choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`)
	}))
	defer gateway.Close()
	s, _ := loadConnection(t, fmt.Sprintf("llm:\n    active_profile: test\n    providers:\n        - id: test\n          base_url: %s\n          api_key: fixture-key\n          model: requested\n", gateway.URL))
	probe := s.CheckModel(context.Background())
	if !probe.Probed || !probe.Available || probe.ReturnedModel != "served-alias" {
		t.Fatalf("probe=%+v", probe)
	}
	if posts != 1 || gets != 0 {
		t.Fatal("probe did not issue exactly one inference")
	}
	_ = s.View()
	if posts != 1 {
		t.Fatal("reading settings issued inference")
	}

}

func TestConnectivityRejectsErrorsAndDoesNotInventReturnedModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"valid_without_model", 200, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`, true},
		{"empty_reply", 200, `{"model":"alias","choices":[{"message":{"content":""}}]}`, false},
		{"only_model_name", 200, `{"model":"alias"}`, false},
		{"rejected_key", 401, `{"error":"fixture-key rejected"}`, false},
		{"invalid_json", 200, `not-json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer gateway.Close()
			s, _ := loadConnection(t, fmt.Sprintf("llm:\n    providers:\n        - id: test\n          base_url: %s\n          api_key: fixture-key\n          model: configured\n", gateway.URL))
			probe := s.CheckModel(context.Background())
			if probe.Available != tc.ok || !probe.Probed {
				t.Fatalf("probe=%+v", probe)
			}
			if probe.ReturnedModel != "" {
				t.Fatal("invented a returned model")
			}
			if strings.Contains(probe.Error, "fixture-key") {
				t.Fatal("diagnostic exposed a saved key")
			}
		})
	}
}

func TestConnectivityUsesAnthropicMessages(t *testing.T) {
	calls := 0
	gateway := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "fixture-key" || r.Header.Get("anthropic-version") == "" {
			t.Error("incorrect Messages request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "requested" || body["tools"] != nil || body["max_tokens"] != float64(256) {
			t.Errorf("incorrect request: %v", body)
		}
		fmt.Fprint(w, `{"model":"served-anthropic","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn"}`)
	}))
	defer gateway.Close()
	s, _ := loadConnection(t, fmt.Sprintf("llm:\n    providers:\n        - id: test\n          provider: anthropic\n          base_url: %s\n          api_key: fixture-key\n          model: requested\n", gateway.URL))
	probe := s.CheckModel(context.Background())
	if !probe.Available || probe.ReturnedModel != "served-anthropic" || calls != 1 {
		t.Fatalf("probe=%+v calls=%d", probe, calls)
	}
}

func loadConnection(t *testing.T, document string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cyber.yaml")
	if err := os.WriteFile(path, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadAt(filepath.Join(dir, "settings.json"), path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}
