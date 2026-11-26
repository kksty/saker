package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/kb"
)

func newKBHub(t *testing.T) *hub {
	t.Helper()
	dir := t.TempDir()
	if _, err := kb.Ensure(dir); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		e := kb.Entry{ID: fmt.Sprintf("fixture-%02d", i), Title: fmt.Sprintf("fixture knowledge %d", i), Description: "fixture", Detail: "fixture fact", Source: "fixture", Tags: []string{"cve"}}
		if i == 0 {
			e.Title = "sqlmap fixture"
		}
		b, _ := json.Marshal(e)
		if err := os.WriteFile(filepath.Join(dir, kb.LocalDir, e.ID+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	lib, err := kb.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &hub{cfg: hubConfig{KB: lib, KBDir: dir}}
}

func TestServeKB(t *testing.T) {
	h := newKBHub(t)

	rec := httptest.NewRecorder()
	h.serveKB(rec, httptest.NewRequest(http.MethodGet, "/api/kb", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Dir     string     `json:"dir"`
		Entries []kb.Entry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Dir != h.cfg.KBDir {
		t.Fatalf("dir = %q, want %q", body.Dir, h.cfg.KBDir)
	}
	if len(body.Entries) != len(h.cfg.KB.Entries()) {
		t.Fatalf("returned %d entries, want complete snapshot", len(body.Entries))
	}
	for _, e := range body.Entries {
		if e.ID == "" || e.Title == "" {
			t.Fatalf("entry without id/title: %+v", e)
		}
	}
}

// TestServeKBSearch covers the query path the commander uses. A bare request
// stays the console's whole-library read (TestServeKB); q/tags/limit are the
// same scored search the old read-only tool was supposed to provide.
func TestServeKBSearch(t *testing.T) {
	h := newKBHub(t)
	total := len(h.cfg.KB.Entries())

	hits := getKBEntries(t, h, "/api/kb?q=sqlmap")
	if len(hits) == 0 || len(hits) >= total {
		t.Fatalf("q=sqlmap returned %d of %d entries", len(hits), total)
	}
	if !strings.Contains(strings.ToLower(hits[0].Title), "sql") {
		t.Fatalf("q=sqlmap top hit = %q", hits[0].Title)
	}

	if got := getKBEntries(t, h, "/api/kb?q=sqlmap&limit=1"); len(got) != 1 {
		t.Fatalf("limit=1 returned %d entries", len(got))
	}

	tagged := getKBEntries(t, h, "/api/kb?tags=cve&limit=3")
	if len(tagged) == 0 || len(tagged) > 3 {
		t.Fatalf("tags=cve returned %d entries", len(tagged))
	}
	for _, e := range tagged {
		hasCVE := false
		for _, tag := range e.Tags {
			if strings.EqualFold(tag, "cve") {
				hasCVE = true
			}
		}
		if !hasCVE {
			t.Fatalf("%s returned for tags=cve without the tag: %+v", e.ID, e.Tags)
		}
	}

	// No q means "first N" for a bare limit; the library's cap is 20.
	if got := getKBEntries(t, h, "/api/kb?limit=50"); len(got) != 20 {
		t.Fatalf("limit=50 returned %d entries, want the 20 cap", len(got))
	}

	rec := httptest.NewRecorder()
	h.serveKB(rec, httptest.NewRequest(http.MethodGet, "/api/kb?q=zzzz-not-in-kb", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("no-match status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"entries":[]`) {
		t.Fatalf("no-match body = %s, want an empty array", rec.Body.Bytes())
	}
}

func TestServeKBTellsBuiltinFromInstanceKnowledge(t *testing.T) {
	h := newKBHub(t)
	type row struct {
		ID            string `json:"id"`
		Origin        string `json:"origin"`
		CoversBuiltin bool   `json:"covers_builtin"`
	}
	read := func() map[string]row {
		rec := httptest.NewRecorder()
		h.serveKB(rec, httptest.NewRequest(http.MethodGet, "/api/kb", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Entries []row `json:"entries"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		out := make(map[string]row, len(body.Entries))
		for _, e := range body.Entries {
			out[e.ID] = e
		}
		return out
	}
	var builtin kb.Entry
	for _, e := range h.cfg.KB.Entries() {
		if e.Origin() == kb.OriginBuiltin {
			builtin = e
			break
		}
	}
	if builtin.ID == "" {
		t.Fatal("fixture has no built-in entry")
	}
	rows := read()
	if rows[builtin.ID].Origin != kb.OriginBuiltin || rows[builtin.ID].CoversBuiltin {
		t.Fatalf("built-in entry = %+v", rows[builtin.ID])
	}
	for id, e := range rows {
		if strings.HasPrefix(id, "fixture-") && e.Origin != kb.OriginInstance {
			t.Fatalf("instance entry %s = %+v", id, e)
		}
		if e.CoversBuiltin {
			t.Fatalf("fresh instance reports an override: %s", id)
		}
	}

	override, err := h.cfg.KB.SaveCandidate(kb.Candidate{
		Entry:        kb.Entry{ID: builtin.ID, Title: "实例覆盖标题", Description: "实例摘要", Detail: "实例正文", Source: "测试"},
		Reason:       "测试实例覆盖",
		BaseRevision: kb.Revision(builtin),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.cfg.KB.Publish(override.ID, override.Version); err != nil {
		t.Fatal(err)
	}
	rows = read()
	if e := rows[builtin.ID]; e.Origin != kb.OriginInstance || !e.CoversBuiltin {
		t.Fatalf("override entry = %+v", e)
	}
}

func getKBEntries(t *testing.T, h *hub, target string) []kb.Entry {
	t.Helper()
	rec := httptest.NewRecorder()
	h.serveKB(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", target, rec.Code, rec.Body.String())
	}
	var body struct {
		Entries []kb.Entry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	return body.Entries
}

func TestServeKBRejectsWritesAndEmptyLibrary(t *testing.T) {
	h := &hub{}
	rec := httptest.NewRecorder()
	h.serveKB(rec, httptest.NewRequest(http.MethodPost, "/api/kb", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.serveKB(rec, httptest.NewRequest(http.MethodGet, "/api/kb", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing library status %d", rec.Code)
	}
}
