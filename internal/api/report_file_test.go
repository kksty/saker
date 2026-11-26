package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/report"
)

// writeReportDir puts one report directory under the case root with a
// report.json of the given status, so the file handler can decide what to serve.
func writeReportDir(t *testing.T, root, id string, meta report.Meta, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, report.Dir, id)
	meta.ID = id
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	files[report.MetaFile] = string(b)
	for rel, body := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A report directory contains the report's markdown plus copies of evidence the
// model chose to include. Those copies are attacker-influenced (a captured page
// can contain script) and are served from the console's own origin, so the
// endpoint must never let the browser treat them as an unsandboxed document.
func TestReportFileSandboxesBrowserDocuments(t *testing.T) {
	root := t.TempDir()
	writeReportDir(t, root, "rep_1", report.Meta{Status: report.StatusReady}, map[string]string{
		"index.md":              "# 报告\n\n- 结论\n",
		"evidence/capture.html": "<script>alert(document.cookie)</script>",
		"evidence/icon.svg":     "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>",
		"evidence/raw.txt":      "plain text",
	})
	s := New(nil, nil, t.TempDir(), root)
	serve := func(id, rel string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/reports/"+id+"/file", nil)
		req.SetPathValue("id", id)
		if rel != "" {
			q := req.URL.Query()
			q.Set("path", rel)
			req.URL.RawQuery = q.Encode()
		}
		rec := httptest.NewRecorder()
		s.serveReportFile(rec, req)
		return rec
	}

	for _, test := range []struct {
		path string
		want string
	}{
		{"evidence/capture.html", "text/html"},
		{"evidence/icon.svg", "image/svg+xml"},
	} {
		res := serve("rep_1", test.path)
		if res.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", test.path, res.Code, res.Body.String())
		}
		if got := res.Header().Get("Content-Type"); got != test.want {
			t.Errorf("%s Content-Type = %q, want %q", test.path, got, test.want)
		}
		if got := res.Header().Get("Content-Security-Policy"); got != "sandbox" {
			t.Errorf("%s CSP = %q, want sandbox (the bytes are attacker-influenced)", test.path, got)
		}
		if got := res.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s nosniff = %q", test.path, got)
		}
	}

	// The report itself stays readable as markdown, and the default path is
	// still index.md (the console opens it without a query).
	for _, rel := range []string{"index.md", ""} {
		res := serve("rep_1", rel)
		if res.Code != http.StatusOK {
			t.Fatalf("markdown %q = %d %s", rel, res.Code, res.Body.String())
		}
		if got := res.Header().Get("Content-Type"); got != "text/markdown; charset=utf-8" {
			t.Errorf("markdown %q = %q", rel, got)
		}
	}

	// A plain-text artifact has no document type; ServeFile may label it, but
	// it is never sniffed into an executable document.
	res := serve("rep_1", "evidence/raw.txt")
	if res.Code != http.StatusOK {
		t.Fatalf("text = %d", res.Code)
	}
	if got := res.Header().Get("Content-Security-Policy"); got != "" {
		t.Errorf("plain text unexpectedly sandboxed: %q", got)
	}
	if got := res.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("plain text nosniff = %q", got)
	}
}

// A report that did not pass its own checks is not a deliverable. Its prose and
// rows stay on disk only so the operator can see why generation failed, and
// checks.json is the one file that must stay readable in every state.
func TestReportFileRefusesReportsThatFailedTheirChecks(t *testing.T) {
	root := t.TempDir()
	failed := report.Meta{
		Status: report.StatusIncomplete,
		Checks: []report.Check{{Rule: "fact_exists", Detail: "f_bogus"}, {Rule: "freshness", OK: true}},
	}
	writeReportDir(t, root, "r-bad", failed, map[string]string{
		"index.md":      "# 报告\n\n这条结论是编的\n",
		"findings.json": `[{"fact":"f_bogus","verdict":"confirmed"}]`,
		"checks.json":   `[{"rule":"fact_exists","ok":false}]`,
	})
	writeReportDir(t, root, "r-draft", report.Meta{Status: report.StatusDraft}, map[string]string{
		"index.md": "# 半截\n",
	})
	s := New(nil, nil, t.TempDir(), root)
	serve := func(id, rel string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/reports/"+id+"/file", nil)
		req.SetPathValue("id", id)
		if rel != "" {
			q := req.URL.Query()
			q.Set("path", rel)
			req.URL.RawQuery = q.Encode()
		}
		rec := httptest.NewRecorder()
		s.serveReportFile(rec, req)
		return rec
	}

	for _, rel := range []string{"index.md", "", "findings.json", "findings/f_x.md"} {
		res := serve("r-bad", rel)
		if res.Code != http.StatusConflict {
			t.Fatalf("incomplete %q = %d %s, want 409", rel, res.Code, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "fact_exists") {
			t.Fatalf("refusal must name the failed rule, got %s", res.Body.String())
		}
	}
	// The diagnosis itself is never withheld: it is how the operator fixes the report.
	if res := serve("r-bad", "checks.json"); res.Code != http.StatusOK {
		t.Fatalf("checks.json = %d %s, want 200", res.Code, res.Body.String())
	}

	for _, rel := range []string{"index.md", ""} {
		res := serve("r-draft", rel)
		if res.Code != http.StatusConflict {
			t.Fatalf("draft %q = %d, want 409", rel, res.Code)
		}
		// checks.json does not exist until the run ends and the checks run, so
		// the refusal must not point the operator at a file that is not there.
		if strings.Contains(res.Body.String(), "checks.json") {
			t.Fatalf("draft refusal must not promise checks.json: %s", res.Body.String())
		}
	}

	// A directory with no report.json cannot claim to be ready either; it must
	// not leak the files of a report that never finished writing its index.
	dir := filepath.Join(root, report.Dir, "r-nometa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/reports/r-nometa/file", nil)
	req.SetPathValue("id", "r-nometa")
	rec := httptest.NewRecorder()
	s.serveReportFile(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("missing report.json = %d %s, want 409", rec.Code, rec.Body.String())
	}
}

// Staleness is computed when the report is read, not when it was written: a
// report that passed its checks against an older ledger must stop being served
// once the ledger moved. The list says stale, so the file handler must agree.
func TestReportFileRefusesStaleReport(t *testing.T) {
	s, st, _ := newBatchServer(t, &batchExec{})
	addVerifyFact(t, st, "vuln", "verified")
	writeReportDir(t, s.CaseRoot, "r-stale", report.Meta{
		Status:       report.StatusReady,
		SourceDigest: "digest-of-an-older-ledger",
	}, map[string]string{
		"index.md":    "# 报告\n\n按旧账本写的\n",
		"checks.json": "[]",
	})
	serve := func(rel string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/reports/r-stale/file", nil)
		req.SetPathValue("id", "r-stale")
		if rel != "" {
			q := req.URL.Query()
			q.Set("path", rel)
			req.URL.RawQuery = q.Encode()
		}
		rec := httptest.NewRecorder()
		s.serveReportFile(rec, req)
		return rec
	}

	res := serve("")
	if res.Code != http.StatusConflict {
		t.Fatalf("stale index = %d %s, want 409", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "stale") {
		t.Fatalf("refusal must say why, got %s", res.Body.String())
	}
	if res := serve("checks.json"); res.Code != http.StatusOK {
		t.Fatalf("stale checks.json = %d, want 200", res.Code)
	}

	// The same report becomes servable again when its digest matches the ledger
	// it was written from, so the refusal is about content, not about a flag.
	writeReportDir(t, s.CaseRoot, "r-stale", report.Meta{
		Status:       report.StatusReady,
		SourceDigest: s.contentDigest(),
	}, map[string]string{
		"index.md":    "# 报告\n",
		"checks.json": "[]",
	})
	if res := serve(""); res.Code != http.StatusOK {
		t.Fatalf("fresh index = %d %s, want 200", res.Code, res.Body.String())
	}
}

// A report written with earlier project documents stays on disk but is no longer deliverable.
func TestReportFileBecomesStaleAfterConstraintsChange(t *testing.T) {
	s, _ := newBindingServer(t)
	s.SetProjectSettings("Starting point", "Old constraints")
	writeReportDir(t, s.CaseRoot, "r-scope", report.Meta{
		Status: report.StatusReady, SourceDigest: s.contentDigest(), ScopeDigest: report.ScopeDigest(s.ReportScope()),
	}, map[string]string{"index.md": "# Previous constraints"})
	writeReportDir(t, s.CaseRoot, "r-legacy-scope", report.Meta{
		Status: report.StatusReady, SourceDigest: s.contentDigest(), CreatedAt: time.Now().Add(-time.Minute),
	}, map[string]string{"index.md": "# Previous constraints"})
	if rec := call(t, s, http.MethodGet, "/api/reports/r-scope/file", nil); rec.Code != http.StatusOK {
		t.Fatalf("initial report = %d %s", rec.Code, rec.Body.String())
	}
	rec := call(t, s, http.MethodPut, "/api/documents/constraints", map[string]any{"text": "New constraints", "revision": 0})
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/reports", "/api/reports/r-scope"} {
		got := call(t, s, http.MethodGet, path, nil)
		if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"status":"stale"`) {
			t.Fatalf("%s = %d %s", path, got.Code, got.Body.String())
		}
	}
	for _, path := range []string{"/api/reports/r-scope/file", "/api/reports/r-legacy-scope/file"} {
		if got := call(t, s, http.MethodGet, path, nil); got.Code != http.StatusConflict {
			t.Fatalf("%s = %d %s", path, got.Code, got.Body.String())
		}
	}
}
