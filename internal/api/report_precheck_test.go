package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/report"
	"github.com/kksty/saker/internal/store"
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
		rows = append(rows, report.Finding{Fact: id, FactClaimVersion: 1, Verdict: report.VerdictConfirmed, Title: "fixture"})
	}
	b, _ := json.Marshal(rows)
	return string(b)
}

// findingsCitingVersion writes one row at an explicit assertion version; 0 is
// the legacy/unknown case.
func findingsCitingVersion(factID string, version uint64) string {
	rows := []report.Finding{{Fact: factID, FactClaimVersion: version, Verdict: report.VerdictConfirmed, Title: "fixture"}}
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

// evidenceFact writes one reportable fact whose evidence row points at real
// bytes under the case root and carries the matching digest.
func evidenceFact(t *testing.T, s *Server, st *store.Store) (model.Fact, model.Evidence, string) {
	t.Helper()
	rel := "in_1/proof.txt"
	abs := filepath.Join(s.CaseRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("admitted proof")
	if err := os.WriteFile(abs, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	ev, err := st.AddEvidence(model.Evidence{
		IntentID: "in_1", URI: rel, Name: "proof.txt", Size: int64(len(body)),
		Digest: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "需要原件的结论", Status: "unverified",
		EvidenceIDs: []string{ev.ID}, Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-proof"}); err != nil {
		t.Fatal(err)
	}
	f, _ = st.FactByID(f.ID)
	return f, ev, abs
}

func evidencePrecheckOf(t *testing.T, mux *http.ServeMux) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/reports/precheck?evidence=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("evidence precheck = %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	raw, ok := out["evidence"].(map[string]any)
	if !ok {
		t.Fatalf("precheck has no evidence scan: %v", out)
	}
	return raw
}

func evidenceRows(t *testing.T, raw map[string]any, key string) []map[string]any {
	t.Helper()
	items, _ := raw[key].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		row, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("%s row = %v", key, it)
		}
		out = append(out, row)
	}
	return out
}

// Generation-time precheck names the broken evidence and the conclusion resting
// on it. The light poll must stay light: only `?evidence=1` performs the hash
// check, and the final generation keeps its own refusal path.
func TestReportPrecheckEvidenceNamesMissingAndAlteredOriginals(t *testing.T) {
	s, st, mux := newBatchServer(t, &batchExec{})
	fact, ev, abs := evidenceFact(t, s, st)

	if raw := evidencePrecheckOf(t, mux); len(evidenceRows(t, raw, "missing")) != 0 || len(evidenceRows(t, raw, "conflicts")) != 0 {
		t.Fatalf("complete evidence was flagged: %v", raw)
	}

	// Light poll: no scan field at all, not "clean".
	if out := precheck(t, mux); out["evidence"] != nil {
		t.Fatalf("the light precheck ran the evidence scan: %v", out)
	}

	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	raw := evidencePrecheckOf(t, mux)
	missing := evidenceRows(t, raw, "missing")
	if len(missing) != 1 || missing[0]["evidence"] != ev.ID || missing[0]["kind"] != report.EvidenceMissing {
		t.Fatalf("missing = %v, want evidence %s", missing, ev.ID)
	}
	facts, _ := missing[0]["fact_ids"].([]any)
	if len(facts) != 1 || facts[0] != fact.ID {
		t.Fatalf("missing issue facts = %v, want %s", facts, fact.ID)
	}

	// Same path, different bytes: a digest conflict is not a missing original.
	if err := os.WriteFile(abs, []byte("altered proof"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw = evidencePrecheckOf(t, mux)
	if len(evidenceRows(t, raw, "missing")) != 0 {
		t.Fatalf("altered bytes reported as missing: %v", raw)
	}
	conflicts := evidenceRows(t, raw, "conflicts")
	if len(conflicts) != 1 || conflicts[0]["evidence"] != ev.ID || conflicts[0]["kind"] != report.EvidenceDigestMismatch {
		t.Fatalf("conflicts = %v, want evidence %s", conflicts, ev.ID)
	}
	facts, _ = conflicts[0]["fact_ids"].([]any)
	if len(facts) != 1 || facts[0] != fact.ID {
		t.Fatalf("conflict facts = %v, want %s", facts, fact.ID)
	}

	// The final generation still uses its own verification: it refuses the same
	// altered original even if the operator never asked for this precheck.
	g := &report.Generator{Store: st, CaseRoot: s.CaseRoot}
	if err := g.Preflight(); err == nil {
		t.Fatal("generation preflight accepted altered bytes")
	}
}

// A report citation is a (fact id, assertion version) pair. A v1 report stays on
// disk and is still findable, but it does not cover v2; a report that cites v2
// is what clears the gap. A pre-version report is version-unknown, never assumed
// to describe the current claim.
func TestReportPrecheckCountsCoverageByClaimVersion(t *testing.T) {
	s, st, mux := newBatchServer(t, &batchExec{})
	fact := addVerifyFact(t, st, "vuln", "verified")
	writeReportFixture(t, s.CaseRoot, "r-v1", report.StatusReady, findingsCitingVersion(fact.ID, 1), true)

	if total, _ := unreportedOf(t, precheck(t, mux)); total != 0 {
		t.Fatalf("a v1 report should cover the v1 fact, total = %v", total)
	}

	// Correct the assertion. The old report may not cover the new claim.
	if _, err := st.UpdateFact(fact.ID, func(f *model.Fact) { f.Summary = "corrected claim" }); err != nil {
		t.Fatal(err)
	}
	// A new assertion starts unverified; the realistic path is that it is
	// re-reviewed before it can be reported again.
	if _, err := st.ReviewFact(model.Review{FactID: fact.ID, Verdict: "pass", VerifierRun: "run-proof-2"}); err != nil {
		t.Fatal(err)
	}
	// addVerifyFact's evidence file is not on disk, but the version gap is
	// computed before generation; the light precheck does not run evidence checks.
	out := precheck(t, mux)
	total, items := unreportedOf(t, out)
	if total != 1 || len(items) != 1 || items[0]["fact"] != fact.ID {
		t.Fatalf("v2 must be unreported, got %v/%v", total, items)
	}
	previous, _ := items[0]["previous"].([]any)
	if len(previous) != 1 {
		t.Fatalf("historical citation lost: %v", items[0])
	}
	row, _ := previous[0].(map[string]any)
	if row["report"] != "r-v1" || row["claim_version"] != float64(1) {
		t.Fatalf("previous = %v, want r-v1 at v1", row)
	}

	// A pre-version report cannot vouch for the current assertion either.
	writeReportFixture(t, s.CaseRoot, "r-legacy", report.StatusReady, findingsCitingVersion(fact.ID, 0), true)
	out = precheck(t, mux)
	total, items = unreportedOf(t, out)
	if total != 1 {
		t.Fatalf("a version-unknown citation must not cover v2, total = %v", total)
	}
	unknown, _ := out["unreported"].(map[string]any)["unknown_version_reports"].([]any)
	if len(unknown) != 1 || unknown[0] != "r-legacy" {
		t.Fatalf("unknown version reports = %v, want [r-legacy]", unknown)
	}

	// Only a report that actually carries v2 clears the gap.
	writeReportFixture(t, s.CaseRoot, "r-v2", report.StatusReady, findingsCitingVersion(fact.ID, 2), true)
	if total, items := unreportedOf(t, precheck(t, mux)); total != 0 {
		t.Fatalf("a v2 report must cover the corrected fact, got %v/%v", total, items)
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
