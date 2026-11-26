package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/report"
)

// writeReportFixture puts one report directory under the case root: report.json
// always, findings.json only when the fixture wants to cite rows.
func writeReportFixture(t *testing.T, caseRoot, id, status, findings string, withFindings bool) {
	t.Helper()
	dir := filepath.Join(caseRoot, report.Dir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(report.Meta{ID: id, Status: status})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, report.MetaFile), meta, 0o644); err != nil {
		t.Fatal(err)
	}
	if !withFindings {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, report.Findings), []byte(findings), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findingsCiting(factIDs ...string) string {
	rows := make([]report.Finding, 0, len(factIDs))
	for _, id := range factIDs {
		rows = append(rows, report.Finding{Fact: id, Verdict: report.VerdictConfirmed, Title: "fixture"})
	}
	b, _ := json.Marshal(rows)
	return string(b)
}

func precheck(t *testing.T, mux *http.ServeMux) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/reports/precheck", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("precheck = %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func unreportedOf(t *testing.T, out map[string]any) (float64, []map[string]any) {
	t.Helper()
	raw, ok := out["unreported"].(map[string]any)
	if !ok {
		t.Fatalf("precheck has no unreported projection: %v", out)
	}
	total, _ := raw["total"].(float64)
	items, _ := raw["items"].([]any)
	list := make([]map[string]any, 0, len(items))
	for _, it := range items {
		row, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("unreported item = %v", it)
		}
		list = append(list, row)
	}
	return total, list
}

// The console's gap view names the verified conclusions no report cites. It is a
// set difference over finding rows: a candidate was never reportable, and a
// fact one report cites is not listed however that report ended.
func TestReportPrecheckNamesVerifiedFactsNoReportCites(t *testing.T) {
	s, st, mux := newBatchServer(t, &batchExec{})
	cited := addVerifyFact(t, st, "vuln", "verified")
	loose := addVerifyFact(t, st, "cred", "verified")
	addVerifyFact(t, st, "vuln", "unverified")
	writeReportFixture(t, s.CaseRoot, "r-stale", report.StatusReady, findingsCiting(cited.ID), true)

	out := precheck(t, mux)
	if out["reportable"] != float64(3) || out["unverified"] != float64(1) {
		t.Fatalf("precheck = %v, want reportable 3 unverified 1", out)
	}
	total, items := unreportedOf(t, out)
	if total != 1 || len(items) != 1 || items[0]["fact"] != loose.ID {
		t.Fatalf("unreported = %v/%v, want only %s", total, items, loose.ID)
	}
	if items[0]["title"] != loose.Title {
		t.Fatalf("unreported title = %v, want %q", items[0]["title"], loose.Title)
	}
	if note, _ := out["unreported"].(map[string]any)["note"].(string); note == "" {
		t.Fatal("unreported projection must state its version boundary")
	}
	if unreadable, _ := out["unreported"].(map[string]any)["unreadable"].([]any); len(unreadable) != 0 {
		t.Fatalf("unreadable = %v, want none", unreadable)
	}
}

// A report whose rows cannot be parsed must not be counted as citing nothing:
// that would present its facts as unreported while the opposite may be true. A
// draft that has not written rows yet is normal, not an error.
func TestReportPrecheckFlagsUnreadableReportRows(t *testing.T) {
	s, st, mux := newBatchServer(t, &batchExec{})
	addVerifyFact(t, st, "vuln", "verified")
	writeReportFixture(t, s.CaseRoot, "r-broken", report.StatusReady, "{not json", true)
	writeReportFixture(t, s.CaseRoot, "r-draft", report.StatusDraft, "", false)

	out := precheck(t, mux)
	total, _ := unreportedOf(t, out)
	if total != 1 {
		t.Fatalf("unreported total = %v, want 1", total)
	}
	raw := out["unreported"].(map[string]any)
	unreadable, _ := raw["unreadable"].([]any)
	if len(unreadable) != 1 || unreadable[0] != "r-broken" {
		t.Fatalf("unreadable = %v, want [r-broken]", unreadable)
	}
}

// A report that failed its own checks is not a delivered citation: the file
// handler refuses it, so its rows must not mark facts as reported. Counting
// them would let a failed check vouch for the very facts it failed on.
func TestReportPrecheckDoesNotCountCitationsFromUnvalidatedReports(t *testing.T) {
	s, st, mux := newBatchServer(t, &batchExec{})
	cited := addVerifyFact(t, st, "vuln", "verified")
	writeReportFixture(t, s.CaseRoot, "r-incomplete", report.StatusIncomplete, findingsCiting(cited.ID), true)
	writeReportFixture(t, s.CaseRoot, "r-draft", report.StatusDraft, findingsCiting(cited.ID), true)

	out := precheck(t, mux)
	total, items := unreportedOf(t, out)
	if total != 1 || len(items) != 1 || items[0]["fact"] != cited.ID {
		t.Fatalf("unreported = %v/%v, want only %s", total, items, cited.ID)
	}
	raw := out["unreported"].(map[string]any)
	uncounted, _ := raw["uncounted"].([]any)
	if len(uncounted) != 2 || uncounted[0] != "r-draft" || uncounted[1] != "r-incomplete" {
		t.Fatalf("uncounted = %v, want the two non-ready reports", uncounted)
	}
	if unreadable, _ := raw["unreadable"].([]any); len(unreadable) != 0 {
		t.Fatalf("unreadable = %v, want none", unreadable)
	}
}

// The projection is an operator view: a session's API must not reach it. The
// route lives only on the console mux, so the handler is absent rather than
// guarded — and this test fails if someone later mounts it on both.
func TestReportPrecheckIsNotOnTheAgentAPI(t *testing.T) {
	f := newAgentFixture(t)
	rec := f.call(t, f.srcID, http.MethodGet, "/api/reports/precheck", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("agent precheck = %d %s, want 404", rec.Code, rec.Body.String())
	}
}

// The list is bounded so one stale engagement cannot push an unbounded row set
// through the console; the total still counts the whole set.
func TestReportPrecheckCapsTheUnreportedList(t *testing.T) {
	_, st, mux := newBatchServer(t, &batchExec{})
	for i := 0; i < unreportedLimit+1; i++ {
		addVerifyFact(t, st, "vuln", "verified")
	}
	out := precheck(t, mux)
	total, items := unreportedOf(t, out)
	if total != float64(unreportedLimit+1) {
		t.Fatalf("unreported total = %v, want %d", total, unreportedLimit+1)
	}
	if len(items) != unreportedLimit {
		t.Fatalf("unreported items = %d, want cap %d", len(items), unreportedLimit)
	}
	raw := out["unreported"].(map[string]any)
	if raw["limit"] != float64(unreportedLimit) {
		t.Fatalf("limit = %v, want %d", raw["limit"], unreportedLimit)
	}
	if _, ok := raw["items"]; !ok {
		t.Fatal(fmt.Sprint("projection must carry items: ", raw))
	}
}
