package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/project"
)

// premise_policy is a fixed mode, not a setting. Every project is verified_only
// and the manifest is normalized to that value on load, so a write here could
// only restate what is already stored while looking like an operator decision.
//
// These assertions are the gate: re-adding a writable path (in this endpoint or
// a separate one) turns them red. The project row still REPORTS the mode, because
// the console and readiness need the value — reporting is not configuring.
func TestRetiredPremisePolicyIsNotWritable(t *testing.T) {
	root := t.TempDir()
	m, err := project.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("Acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	h := &hub{mgr: m, kits: map[string]*kit{}}

	put := func(hh *hub, body string) (int, map[string]any) {
		r := httptest.NewRequest(http.MethodPut, "/api/projects/"+p.ID, strings.NewReader(body))
		w := httptest.NewRecorder()
		hh.updateProject(w, r, p.ID)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// 1. The fixed mode is still reported for display.
	if got := h.info(m.Get(p.ID)).PremisePolicy; got != project.PremisePolicyVerifiedOnly {
		t.Fatalf("fresh project premise_policy = %q, want %q", got, project.PremisePolicyVerifiedOnly)
	}

	// 2. No form of the field is accepted, including the value that is already
	// in effect: "保存成功" on a no-op is the lie being removed.
	for _, body := range []string{
		`{"premise_policy":"verified_only"}`,
		`{"premise_policy":"standard"}`,
		`{"premise_policy":"strict"}`,
		`{"premise_policy":"sometimes"}`,
		`{"premise_policy":null}`,
	} {
		code, out := put(h, body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s = %d %v, want 400", body, code, out)
		}
		if msg, _ := out["error"].(string); !strings.Contains(msg, "已固定") {
			t.Fatalf("%s rejection does not explain the fixed mode: %v", body, out)
		}
	}

	// 3. The rejection happens before any setter: a mixed request must not land
	// the earlier field behind a 400.
	code, _ := put(h, `{"max_runs":3,"premise_policy":"verified_only"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("mixed save = %d, want 400", code)
	}
	if got, _ := m.Get(p.ID).MaxRunsOverride(); got != 0 {
		t.Fatalf("max_runs changed on a rejected save: %v", got)
	}

	// 4. Omitting the field is the normal save path and still reports the mode.
	code, out := put(h, `{"max_runs":3}`)
	if code != http.StatusOK || out["premise_policy"] != project.PremisePolicyVerifiedOnly {
		t.Fatalf("omitted policy = %d %v", code, out)
	}

	// 4b. The scope and constraint documents have exactly one write entry now:
	// this settings endpoint refuses them instead of bypassing the revision.
	for _, body := range []string{`{"scope":"changed.example"}`, `{"description":"changed"}`} {
		code, out := put(h, body)
		if code != http.StatusBadRequest || out["code"] != "document_write_moved" {
			t.Fatalf("%s = %d %v, want 400 document_write_moved", body, code, out)
		}
	}
	if got := m.Get(p.ID).ScopeText(); got != "acme.example" {
		t.Fatalf("settings endpoint changed the scope: %q", got)
	}

	// 5. A legacy standard/strict manifest is still normalized on load and the
	// stored value is rewritten, so disk never claims a mode that cannot run.
	path := filepath.Join(p.Dir, project.ManifestFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["premise_policy"] = "strict"
	legacy, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	legacyMgr, err := project.Open(root)
	if err != nil {
		t.Fatalf("a legacy premise_policy must still load: %v", err)
	}
	legacyProject := legacyMgr.Get(p.ID)
	if legacyProject == nil {
		t.Fatalf("legacy project %s was not loaded", p.ID)
	}
	legacyHub := &hub{mgr: legacyMgr, kits: map[string]*kit{}}
	if got := legacyHub.info(legacyProject).PremisePolicy; got != project.PremisePolicyVerifiedOnly {
		t.Fatalf("legacy strict premise_policy = %q, want verified_only", got)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), `"premise_policy": "verified_only"`) {
		t.Fatalf("legacy manifest was not normalized:\n%s", stored)
	}
}
