package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kksty/saker/internal/testutil"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/settings"
)

// Without a managed local host the console must still probe the saved
// connection directly (audit instance / console-only deployment).
func TestCheckModelFallsBackToSelfProbeWithoutHost(t *testing.T) {
	gateway := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "requested-model" {
			t.Errorf("probe model = %v", body["model"])
		}
		fmt.Fprint(w, `{"model":"served-alias","choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`)
	}))
	defer gateway.Close()
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	body := "llm:\n  base_url: " + gateway.URL + "\n  api_key: fixture-key\n  model: requested-model\n"
	if err := os.WriteFile(harness, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := settings.LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatal(err)
	}
	h := &hub{settings: st}
	probe := h.checkModel(context.Background())
	if !probe.Probed || !probe.Available || probe.Source != "self" || probe.ReturnedModel != "served-alias" {
		t.Fatalf("fallback probe = %+v", probe)
	}
}
