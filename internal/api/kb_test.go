package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/kb"
)

// The hit list names entries and says what opening one costs, but ships no
// body: the caller pays for the entry it actually opens, with ?id=.
func TestWorldKBHitListIsBriefAndOneEntryOpensByID(t *testing.T) {
	s, _ := newBindingServer(t)
	dir := t.TempDir()
	if _, err := kb.Ensure(dir); err != nil {
		t.Fatal(err)
	}
	lib, err := kb.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.KB = lib
	c, err := lib.SaveCandidate(kb.Candidate{
		Entry: kb.Entry{
			ID:          "kb900",
			Title:       "检索命中样例",
			Description: "一行摘要",
			Detail:      "正文第一段\n" + strings.Repeat("正文内容。", 80),
			Commands:    []string{"echo a", "echo b"},
			Tags:        []string{"web"},
			Source:      "现场",
		},
		Reason: "跨项目可用",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Publish(c.ID, c.Version); err != nil {
		t.Fatal(err)
	}

	hits := call(t, s, http.MethodGet, "/api/kb?q=检索命中样例", nil)
	if hits.Code != http.StatusOK {
		t.Fatal(hits.Body.String())
	}
	var list struct {
		Entries   []map[string]any  `json:"entries"`
		Revisions map[string]string `json:"revisions"`
	}
	if err := json.Unmarshal(hits.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Entries) != 1 {
		t.Fatalf("hits = %d, want 1: %s", len(list.Entries), hits.Body.String())
	}
	hit := list.Entries[0]
	if hit["id"] != "kb900" || hit["title"] != "检索命中样例" {
		t.Fatalf("hit identity = %v", hit)
	}
	if hit["has_detail"] != true || hit["n_commands"] != float64(2) {
		t.Fatalf("hit did not announce its body: %v", hit)
	}
	if _, ok := hit["detail"]; ok {
		t.Fatal("the hit list shipped the body")
	}
	if list.Revisions["kb900"] == "" {
		t.Fatal("the hit list lost the edit token")
	}

	opened := call(t, s, http.MethodGet, "/api/kb?id=kb900", nil)
	if opened.Code != http.StatusOK {
		t.Fatal(opened.Body.String())
	}
	var one struct {
		Entry    kb.Entry `json:"entry"`
		Revision string   `json:"revision"`
	}
	if err := json.Unmarshal(opened.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	if one.Entry.Detail == "" || one.Entry.Title != "检索命中样例" || one.Revision == "" {
		t.Fatalf("opened entry = %+v revision %q", one.Entry, one.Revision)
	}

	missing := call(t, s, http.MethodGet, "/api/kb?id=kb-missing", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing entry status = %d, want 404", missing.Code)
	}
}

func TestParticipantNominatesWithServerIdentityAndReadsLiveWorldKnowledge(t *testing.T) {
	s, _ := newBindingServer(t)
	dir := t.TempDir()
	if _, err := kb.Ensure(dir); err != nil {
		t.Fatal(err)
	}
	lib, err := kb.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.KB = lib
	req := httptest.NewRequest("POST", "/api/kb/candidates", strings.NewReader(`{"entry":{"id":"world-peer","title":"共同协议事实","description":"核对条件","detail":"事实内容","source":"协议规范"},"reason":"跨项目成立","origin":"forged","published":true}`))
	req = req.WithContext(WithIdentity(req.Context(), collab.Identity{ProjectID: "actual", ParticipantID: "pt_a", RunID: "r_a"}))
	rec := httptest.NewRecorder()
	s.AgentHandler().ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatal(rec.Body.String())
	}
	var c kb.Candidate
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Published || c.Origin != "project:actual participant:pt_a run:r_a" {
		t.Fatal("caller forged publication or source")
	}
	before := call(t, s, http.MethodGet, "/api/kb?q=共同协议事实", nil)
	if !strings.Contains(before.Body.String(), `"entries":[]`) {
		t.Fatal("draft became visible as world knowledge")
	}
	if _, err := lib.Publish(c.ID, c.Version); err != nil {
		t.Fatal(err)
	}
	after := call(t, s, http.MethodGet, "/api/kb?q=共同协议事实", nil)
	if !strings.Contains(after.Body.String(), "world-peer") {
		t.Fatal("running project did not see publication")
	}
	var response struct {
		Revisions map[string]string `json:"revisions"`
	}
	if err := json.Unmarshal(after.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Revisions["world-peer"] == "" {
		t.Fatal("reader did not get an edit token")
	}
	stale := call(t, s, http.MethodPost, "/api/kb/candidates", map[string]any{"entry": c.Entry, "reason": c.Reason, "base_revision": "stale"})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale edit status=%d: %s", stale.Code, stale.Body.String())
	}
}
