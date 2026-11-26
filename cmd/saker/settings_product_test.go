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

func settingsPUT(t *testing.T, h *hub, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.serveSettings(rec, req)
	return rec
}

// The product sections are saved under extensions (the only place the harness
// accepts a section the hub itself does not declare) and the response reports
// that the saved hostless configuration needs no restart.
func TestSettingsSaveWritesProductExtensions(t *testing.T) {
	h, harness := singleModelHub(t)
	body := `{"base_url":"http://fast:8080","model":"fast-model",
		"mcp_servers":{"ida":{"command":"idalib-mcp","args":["--stdio"]},
		               "docs":{"url":"https://example.test/mcp","headers":{"Authorization":"Bearer secret"}}},
		"search_tavily_key":"tv-one,tv-two"}`
	rec := settingsPUT(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings save = %d %s", rec.Code, rec.Body.String())
	}
	var view settings.View
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Applied != "saved" || view.RequiresRestart {
		t.Fatalf("apply state = %+v", view)
	}
	if view.McpServers["ida"].Command != "idalib-mcp" || len(view.McpServers["ida"].Args) != 1 {
		t.Fatalf("mcp view = %+v", view.McpServers)
	}
	if got := view.McpServers["docs"].Headers["Authorization"]; got != "***" {
		t.Fatalf("secret header echoed unmasked: %q", got)
	}
	if !view.SearchHasKey || view.SearchKeyCount != 2 {
		t.Fatalf("search view = %+v", view)
	}
	if strings.Contains(rec.Body.String(), "tv-one") || strings.Contains(rec.Body.String(), "Bearer secret") {
		t.Fatalf("response echoed a secret: %s", rec.Body.String())
	}
	data, err := os.ReadFile(harness)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"extensions:", "mcpServers:", "idalib-mcp", "tavily_keys: tv-one,tv-two", "model: fast-model"} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved harness config missing %q:\n%s", want, text)
		}
	}
	for _, wrong := range []string{"\nmcp:", "\nsearch:"} {
		if strings.Contains(text, wrong) {
			t.Fatalf("product section written at the rejected top level:\n%s", text)
		}
	}

	// The GET view is masked the same way and never contains the search key.
	get := httptest.NewRecorder()
	h.serveSettings(get, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), "tv-one") {
		t.Fatalf("settings read leaked a key: %d %s", get.Code, get.Body.String())
	}
}

func TestSettingsSaveRejectsInvalidMCPBeforePersisting(t *testing.T) {
	h, harness := singleModelHub(t)
	before, err := os.ReadFile(harness)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"base_url":"http://fast:8080","model":"fast-model","mcp_servers":{"bad alias":{"command":"x"}}}`
	rec := settingsPUT(t, h, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid product = %d %s", rec.Code, rec.Body.String())
	}
	after, err := os.ReadFile(harness)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("a rejected save changed the harness file:\n%s", after)
	}
}

// A save that does not touch the product sections keeps their stored values.
func TestSettingsModelSaveKeepsProductSections(t *testing.T) {
	h, harness := singleModelHub(t)
	if rec := settingsPUT(t, h, `{"base_url":"http://fast:8080","model":"fast-model","mcp_servers":{"ida":{"command":"idalib-mcp"}},"search_tavily_key":"k"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed save = %d %s", rec.Code, rec.Body.String())
	}
	rec := settingsPUT(t, h, `{"base_url":"http://fast:8080","model":"fast-model","context_window":200000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("model-only save = %d %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(harness)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "idalib-mcp") || !strings.Contains(string(data), "tavily_keys: k") {
		t.Fatalf("model-only save dropped the product sections:\n%s", data)
	}
}

func TestSettingsResponseDistinguishesSavedModelFromFailedApply(t *testing.T) {
	h, _ := offlineRetentionHub(t)
	// A regular file in place of the runtime directory makes host configuration
	// generation fail after the model configuration has already been saved.
	h.cfg.DataDir = filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(h.cfg.DataDir, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	rec := settingsPUT(t, h, `{"base_url":"http://gateway.test/v1","model":"next-model","api_key":"fixture-private-key"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Code     string        `json:"code"`
		Settings settings.View `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Code != "settings_saved_apply_failed" || result.Settings.Model != "next-model" {
		t.Fatalf("response did not identify the saved model: code=%q model=%q", result.Code, result.Settings.Model)
	}
	if !result.Settings.APIKeySet || strings.Contains(rec.Body.String(), "fixture-private-key") {
		t.Fatal("the persisted settings in an apply failure must mask the key")
	}
	if current, _ := settingsGET(t, h); current.Model != result.Settings.Model {
		t.Fatalf("GET model = %q, PUT failure reported %q", current.Model, result.Settings.Model)
	}
}
