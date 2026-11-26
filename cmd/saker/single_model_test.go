package main

import (
	"github.com/kksty/saker/internal/settings"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRetiredModelManagementRoutesAreGone(t *testing.T) {
	h, _ := singleModelHub(t)
	for _, path := range []string{"/api/model-profiles", "/api/model-profiles/old/rotate", "/api/model-catalogue"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 404 {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func singleModelHub(t *testing.T) (*hub, string) {
	t.Helper()
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	body := `llm:
    active_profile: fast
    providers:
        - id: fast
          provider: openai
          base_url: http://fast:8080
          api_key: fast-secret
          model: fast-model
`
	if err := os.WriteFile(harness, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := settings.LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatal(err)
	}
	return &hub{settings: st}, harness
}
