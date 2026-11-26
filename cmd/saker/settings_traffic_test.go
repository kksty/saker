package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/node"
	"github.com/kksty/saker/internal/settings"
)

func settingsGET(t *testing.T, h *hub) (settings.View, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.serveSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d %s", rec.Code, rec.Body.String())
	}
	var view settings.View
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("GET /api/settings is not the documented view: %v", err)
	}
	return view, rec.Body.String()
}

// offlineRetentionHub is a settings host without a managed local host, with a
// data dir and a node registry so the generated scan runtime document is
// actually produced on save. That is the "scan is not running here" case.
func offlineRetentionHub(t *testing.T) (*hub, string) {
	t.Helper()
	h, harness := singleModelHub(t)
	h.cfg.DataDir = t.TempDir()
	h.cfg.Nodes = &node.Registry{}
	return h, harness
}

func readHarnessText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func runtimeYAMLFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dataDir, "host", "config", "runtime-*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// The console reads the effective retention budget plus the metadata its form
// needs: default, allowed range, whether an operator value exists and where the
// effective value came from. These names are the front-end contract.
func TestSettingsTrafficRetentionDefaultView(t *testing.T) {
	h, _ := singleModelHub(t)
	view, raw := settingsGET(t, h)

	if view.TrafficBodyRetentionBytes == nil {
		t.Fatal("GET /api/settings did not return traffic_body_retention_bytes")
	}
	if got := *view.TrafficBodyRetentionBytes; got != 0 {
		t.Fatalf("traffic_body_retention_bytes = %d, want 0 (no explicit override)", got)
	}
	if view.TrafficBodyRetentionEffectiveBytes != 2<<30 {
		t.Fatalf("traffic_body_retention_effective_bytes = %d, want default 2 GiB", view.TrafficBodyRetentionEffectiveBytes)
	}
	if view.TrafficBodyRetentionDefault != 2<<30 {
		t.Fatalf("traffic_body_retention_default = %d", view.TrafficBodyRetentionDefault)
	}
	if view.TrafficBodyRetentionMin != 2*(8<<20) || view.TrafficBodyRetentionMax != 1<<40 {
		t.Fatalf("range = %d..%d, want 16 MiB..1 TiB", view.TrafficBodyRetentionMin, view.TrafficBodyRetentionMax)
	}
	if view.TrafficBodyRetentionExplicit {
		t.Fatal("default reported as explicit")
	}
	if !strings.Contains(view.TrafficBodyRetentionSource, "默认") {
		t.Fatalf("source = %q, want a readable default explanation", view.TrafficBodyRetentionSource)
	}
	for _, key := range []string{
		`"traffic_body_retention_bytes":`, `"traffic_body_retention_effective_bytes":`,
		`"traffic_body_retention_default":`, `"traffic_body_retention_min_bytes":`,
		`"traffic_body_retention_max_bytes":`, `"traffic_body_retention_explicit":`,
		`"traffic_body_retention_source":`,
	} {
		if !strings.Contains(raw, key) {
			t.Fatalf("GET /api/settings lacks %s: %s", key, raw)
		}
	}
}

// A save changes traffic.body_retention_bytes in the operator cyber.yaml (other
// traffic fields and the model stay intact), regenerates the scan runtime
// document, and reports that the setting applies at the next host start because
// no host is running here.
func TestSettingsTrafficRetentionSaveOffline(t *testing.T) {
	h, harness := offlineRetentionHub(t)
	body := `{"base_url":"http://fast:8080","model":"fast-model",
		"traffic_body_retention_bytes":4294967296}`
	rec := settingsPUT(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings save = %d %s", rec.Code, rec.Body.String())
	}
	var view settings.View
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Applied != "saved" {
		t.Fatalf("applied = %q, want saved (no running host)", view.Applied)
	}
	if view.RequiresRestart {
		t.Fatalf("offline save reported requires_restart: %+v", view)
	}
	if !strings.Contains(view.RestartReason, "下次启动") {
		t.Fatalf("restart_reason = %q, want the next-start explanation", view.RestartReason)
	}
	if view.TrafficBodyRetentionBytes == nil || *view.TrafficBodyRetentionBytes != 4<<30 {
		t.Fatalf("view bytes = %v, want 4 GiB", view.TrafficBodyRetentionBytes)
	}
	if !view.TrafficBodyRetentionExplicit {
		t.Fatal("saved value reported as not explicit")
	}

	text := readHarnessText(t, harness)
	for _, want := range []string{"traffic:", "body_retention_bytes: 4294967296", "model: fast-model"} {
		if !strings.Contains(text, want) {
			t.Fatalf("operator cyber.yaml missing %q:\n%s", want, text)
		}
	}
	files := runtimeYAMLFiles(t, h.cfg.DataDir)
	if len(files) != 1 {
		t.Fatalf("runtime yaml files = %v, want one regenerated file", files)
	}
	if !strings.Contains(readHarnessText(t, files[0]), "body_retention_bytes: 4294967296") {
		t.Fatalf("scan runtime document did not pick up the saved retention:\n%s", readHarnessText(t, files[0]))
	}

	get, _ := settingsGET(t, h)
	if get.TrafficBodyRetentionBytes == nil || *get.TrafficBodyRetentionBytes != 4<<30 || !get.TrafficBodyRetentionExplicit {
		t.Fatalf("GET after save = %+v", get)
	}
}

// Values the harness/console would reject are 400 before anything is written,
// and an already saved retention budget stays untouched.
func TestSettingsTrafficRetentionInvalid(t *testing.T) {
	h, harness := offlineRetentionHub(t)
	if rec := settingsPUT(t, h, `{"base_url":"http://fast:8080","model":"fast-model","traffic_body_retention_bytes":8589934592}`); rec.Code != http.StatusOK {
		t.Fatalf("seed save = %d %s", rec.Code, rec.Body.String())
	}
	before := readHarnessText(t, harness)

	cases := []struct{ name, body, hint string }{
		{"below minimum", `{"base_url":"http://fast:8080","model":"fast-model","traffic_body_retention_bytes":1048576}`, "至少"},
		{"above cap", `{"base_url":"http://fast:8080","model":"fast-model","traffic_body_retention_bytes":1099511627777}`, "1 TiB"},
		{"negative", `{"base_url":"http://fast:8080","model":"fast-model","traffic_body_retention_bytes":-1}`, "负数"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := settingsPUT(t, h, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("invalid save = %d %s, want 400", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.hint) {
				t.Fatalf("error %s does not mention %q", rec.Body.String(), c.hint)
			}
		})
	}
	if after := readHarnessText(t, harness); after != before {
		t.Fatalf("a rejected save changed the operator file:\n%s", after)
	}
}

// Saving the model and the retention budget together persists both; a later
// model-only save (the retention key omitted) must not clear it; an explicit 0
// removes the override.
func TestSettingsCombinedSaveKeepsTrafficRetention(t *testing.T) {
	h, harness := offlineRetentionHub(t)
	combined := `{"base_url":"http://fast:8080","model":"fast-model","context_window":200000,
		"traffic_body_retention_bytes":68719476736,
		"mcp_servers":{"ida":{"command":"idalib-mcp"}}}`
	if rec := settingsPUT(t, h, combined); rec.Code != http.StatusOK {
		t.Fatalf("combined save = %d %s", rec.Code, rec.Body.String())
	}
	text := readHarnessText(t, harness)
	for _, want := range []string{"context_window: 200000", "body_retention_bytes: 68719476736", "idalib-mcp"} {
		if !strings.Contains(text, want) {
			t.Fatalf("combined save missing %q:\n%s", want, text)
		}
	}

	// A later model-only save must leave the traffic value and product sections
	// alone instead of interpreting a missing key as 0 (reset).
	if rec := settingsPUT(t, h, `{"base_url":"http://fast:8080","model":"fast-model","context_window":200000}`); rec.Code != http.StatusOK {
		t.Fatalf("model-only save = %d %s", rec.Code, rec.Body.String())
	}
	text = readHarnessText(t, harness)
	for _, want := range []string{"body_retention_bytes: 68719476736", "idalib-mcp"} {
		if !strings.Contains(text, want) {
			t.Fatalf("model-only save dropped %q:\n%s", want, text)
		}
	}

	// Explicit 0 means "use the default", and removes the operator override.
	rec := settingsPUT(t, h, `{"base_url":"http://fast:8080","model":"fast-model","context_window":200000,"traffic_body_retention_bytes":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset save = %d %s", rec.Code, rec.Body.String())
	}
	var view settings.View
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.TrafficBodyRetentionExplicit {
		t.Fatal("0 did not reset to the default")
	}
	if view.TrafficBodyRetentionBytes == nil || *view.TrafficBodyRetentionBytes != 0 {
		t.Fatalf("bytes after reset = %v, want 0 (no explicit override)", view.TrafficBodyRetentionBytes)
	}
	if view.TrafficBodyRetentionEffectiveBytes != 2<<30 {
		t.Fatalf("effective bytes after reset = %d, want 2 GiB", view.TrafficBodyRetentionEffectiveBytes)
	}
	if strings.Contains(readHarnessText(t, harness), "body_retention_bytes:") {
		t.Fatalf("reset left an explicit retention value:\n%s", readHarnessText(t, harness))
	}
}
