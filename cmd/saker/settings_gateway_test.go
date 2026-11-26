package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type settingsGatewayJSON struct {
	Gateway struct {
		Instances       int    `json:"instances"`
		Enabled         int    `json:"enabled"`
		ProxyConfigured bool   `json:"proxy_configured"`
		Editable        bool   `json:"editable"`
		Document        string `json:"document"`
		Reason          string `json:"reason"`
	} `json:"gateway"`
	Applied         string `json:"applied"`
	RequiresRestart bool   `json:"requires_restart"`
	RestartReason   string `json:"restart_reason"`
}

func settingsGatewayGet(t *testing.T, h *hub) settingsGatewayJSON {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d %s", rec.Code, rec.Body.String())
	}
	var got settingsGatewayJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// The gateway panel edits the same section the scan loads. The read must carry
// the masked YAML (so instances can be added or disabled) and never a stored
// credential.
func TestSettingsGETExposesEditableGatewayDocument(t *testing.T) {
	h := settingsHubWith(t, `gateway:
  fofa:
    base_url: https://gw.test/fofa
    key: fixture-gateway-secret
cyberhub:
  proxy: office
`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d %s", rec.Code, rec.Body.String())
	}
	got := settingsGatewayGet(t, h)
	if !got.Gateway.Editable || got.Gateway.Instances != 1 || !got.Gateway.ProxyConfigured {
		t.Fatalf("gateway = %+v", got.Gateway)
	}
	for _, want := range []string{"gateway:", "fofa:", "https://gw.test/fofa", "proxy: office"} {
		if !strings.Contains(got.Gateway.Document, want) {
			t.Fatalf("document lacks %q:\n%s", want, got.Gateway.Document)
		}
	}
	if strings.Contains(rec.Body.String(), "fixture-gateway-secret") {
		t.Fatalf("settings view leaked the gateway key: %s", rec.Body.String())
	}
}

// A save keeps the masked credential, rewrites only the edited fields, and
// reports the restart the scan-start-only section needs.
func TestSettingsPUTGatewaySavesAndReportsRestart(t *testing.T) {
	h := settingsHubWith(t, `gateway:
  fofa:
    base_url: https://old.test/fofa
    key: fixture-gateway-secret
cyberhub:
  proxy: office
`)
	document := settingsGatewayGet(t, h).Gateway.Document
	body, err := json.Marshal(map[string]string{
		"document": strings.Replace(document, "https://old.test/fofa", "https://new.test/fofa", 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/settings/gateway", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	var got settingsGatewayJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Applied != "saved" || got.RequiresRestart || got.RestartReason == "" {
		t.Fatalf("save outcome = %+v", got)
	}
	if strings.Contains(rec.Body.String(), "fixture-gateway-secret") {
		t.Fatalf("save response echoed the key: %s", rec.Body.String())
	}
	saved, err := os.ReadFile(h.settings.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://new.test/fofa", "fixture-gateway-secret", "proxy: office"} {
		if !strings.Contains(string(saved), want) {
			t.Fatalf("saved file lacks %q:\n%s", want, saved)
		}
	}
	if strings.Contains(string(saved), "***") {
		t.Fatalf("mask was written to disk:\n%s", saved)
	}
	if second := settingsGatewayGet(t, h).Gateway.Document; !strings.Contains(second, "https://new.test/fofa") {
		t.Fatalf("read did not reflect the save:\n%s", second)
	}
}

func TestSettingsPUTGatewayRejectsInvalidDocument(t *testing.T) {
	h := settingsHubWith(t, "gateway:\n  fofa:\n    key: stored-key\n")
	before, err := os.ReadFile(h.settings.Path())
	if err != nil {
		t.Fatal(err)
	}
	for name, document := range map[string]string{
		"unknown section":      "guardrail:\n  provider: jev\n",
		"masked with no value": "gateway:\n  hunter:\n    key: '***'\n",
		"string enabled":       "gateway:\n  fofa:\n    enabled: 'false'\n",
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(map[string]string{"document": document})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/settings/gateway", strings.NewReader(string(body))))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("PUT = %d %s, want 400", rec.Code, rec.Body.String())
			}
		})
	}
	if after, err := os.ReadFile(h.settings.Path()); err != nil || string(after) != string(before) {
		t.Fatalf("rejected saves rewrote the file: %v\n%s", err, after)
	}
}

func TestSettingsGatewayEndpointIsPutOnly(t *testing.T) {
	h := settingsHubWith(t, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings/gateway", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/settings/gateway = %d, want 405", rec.Code)
	}
}
