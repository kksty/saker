package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/project"
)

// verify_auto is retired, not defaulted. Reviewing is fixed by the premise
// policy: a direction's conclusions are reviewed at settlement and the pump
// serves cited premises, so there is no switch to flip, no instance default to
// inherit and no effective value to report.
//
// These assertions are the gate. Re-adding the field to the wire shape, the
// instance default, or a writable path turns them red — the failure this locks
// out is a console control that stores a value nothing reads.
func TestRetiredVerifyAutoIsNeitherAdvertisedNorWritable(t *testing.T) {
	m, err := project.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Create("Acme", "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	h := &hub{mgr: m, cfg: hubConfig{}, kits: map[string]*kit{}}

	// 1. A project row must not describe a review switch.
	raw, err := json.Marshal(h.info(m.Get(p.ID)))
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"verify_auto", "verify_auto_effective"} {
		if _, ok := row[key]; ok {
			t.Fatalf("project row still reports %q: %s", key, raw)
		}
	}

	// 2. The project index must not offer an instance default to inherit. The
	// sibling defaults stay, so this is not passing on an empty response.
	w := httptest.NewRecorder()
	h.serveProjects(w, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	var index map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &index); err != nil {
		t.Fatalf("index = %s", w.Body.String())
	}
	if _, ok := index["verify_auto_default"]; ok {
		t.Fatalf("project index still carries verify_auto_default: %s", w.Body.String())
	}
	if _, ok := index["round_timeout_seconds"]; !ok {
		t.Fatalf("index lost its sibling defaults, assertion would be vacuous: %s", w.Body.String())
	}

	// 3. Every form of the retired key is rejected before anything is persisted.
	put := func(body string) (int, map[string]any) {
		r := httptest.NewRequest(http.MethodPut, "/api/projects/"+p.ID, strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.updateProject(rec, r, p.ID)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	for _, body := range []string{
		`{"verify_auto":true}`,
		`{"verify_auto":false}`,
		`{"verify_auto":null}`,
		`{"verify_auto":"yes"}`,
		`{"scope":"changed","verify_auto":true}`,
	} {
		code, out := put(body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s = %d %v, want 400", body, code, out)
		}
		if msg, _ := out["error"].(string); !strings.Contains(msg, "已废弃") {
			t.Fatalf("%s rejection does not explain retirement: %v", body, out)
		}
	}
	if got := m.Get(p.ID).ScopeText(); got != "acme.example" {
		t.Fatalf("a rejected verify_auto save changed the scope: %q", got)
	}
}
