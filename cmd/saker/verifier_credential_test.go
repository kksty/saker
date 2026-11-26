package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/saker/internal/api"
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/evidence"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/project"
)

// A review run works in a fresh, empty directory, so it has to reach the proof it
// was asked to re-prove through the control plane. Its credential is a third
// role: battlefield reads (the fact, the evidence it cites, what a run did) and
// nothing else — not the board it would start second-guessing from, not another
// reviewer's verdict it could copy, and not a single write route. Everything
// outside that surface is absent (404), never 403, so a reviewer cannot learn
// which other surfaces exist.
func TestVerifierReadOnlyCredentialReachesOnlyTheReadSurface(t *testing.T) {
	root := t.TempDir()
	mgr, err := project.Open(filepath.Join(root, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("review-read", "")
	if err != nil {
		t.Fatal(err)
	}

	intent, err := p.Store.CreateIntent(model.Intent{Title: "被复核的意图"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.Store.AddRun(model.Run{IntentID: intent.ID, Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.UpdateRun(run.ID, func(r *model.Run) { r.Status = model.RunDone }); err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(p.Dir, "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same rule as any run journal read: the line must carry the schema version
	// this build writes, otherwise the reader reports "旧格式不支持" instead of
	// guessing the shape (journal.go versionedJournalLine).
	journal := fmt.Sprintf(`{"v":%d,"kind":"tool_result","call_id":"call_1","raw":{"tool_result":{"output":[{"text":{"text":"200 OK"}}]}}}`, executor.JournalSchemaVersion) + "\n"
	if err := os.WriteFile(filepath.Join(runs, run.ID+".jsonl"), []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	// A second run of the same kind the credential belongs to: another review's
	// journal must stay absent, because reading the previous reviewer's reasoning
	// is what turns an independent recomputation into agreement.
	supportRun, err := p.Store.AddRun(model.Run{Purpose: "verifier", Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runs, supportRun.ID+".jsonl"), []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}

	// The original bytes and the ledger row that admitted them, so the evidence
	// endpoint is the real one rather than a 404 that would pass for "allowed".
	rel := filepath.Join(intent.ID, "proof.html")
	src := filepath.Join(p.CaseDir, rel)
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("HTTP/1.1 200 OK\nadmin visible"), 0o644); err != nil {
		t.Fatal(err)
	}
	preserved, err := evidence.Preserve(p.CaseDir, src)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := p.Store.AddEvidence(model.Evidence{
		RunID: run.ID, IntentID: intent.ID, Name: "proof.html",
		Size: preserved.Size, URI: filepath.ToSlash(rel), Digest: preserved.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := p.Store.AddFact(model.Fact{
		IntentID: intent.ID, Kind: "vuln", Title: "/admin 越权",
		Summary: "普通用户可读订单", Doc: "GET /admin (Cookie uid=2) → 200",
		EvidenceIDs: []string{ev.ID}, Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := api.New(p.Store, nil, p.Dir, p.CaseDir)
	mux := http.NewServeMux()
	srv.Register(mux)
	// The participant entrance (/agent/) authenticates against the same registry,
	// so the fixture needs it too: otherwise it answers "not enabled" before the
	// credential is ever looked at.
	registry := collab.NewRegistry(time.Hour)
	h := &hub{mgr: mgr, cfg: hubConfig{Collab: registry}, kits: map[string]*kit{p.ID: {proj: p, srv: srv, mux: mux}}}

	token, err := registry.IssueReadOnly(p.ID, "r_verify")
	if err != nil {
		t.Fatal(err)
	}
	handler := newConsoleAuth("console-token", registry).wrap(h)

	do := func(method, token, path string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	prefix := "/p/" + p.ID + "/api"

	// The surface the reviewer is given: the fact, its history, its evidence and
	// what a run actually did.
	for _, path := range []string{
		prefix + "/facts",
		prefix + "/facts/" + f.ID,
		prefix + "/facts/" + f.ID + "/history",
		prefix + "/evidence",
		prefix + "/evidence/" + ev.ID,
		prefix + "/runs/" + run.ID + "/events",
	} {
		if code, body := do(http.MethodGet, token, path); code != http.StatusOK {
			t.Errorf("review GET %s = %d %q, want 200", path, code, body)
		}
	}

	// Absent, not forbidden. /api/reviews is another reviewer's verdict, /board
	// and /intents are what the party under review believes, /messages is calling
	// out, and /output + /context stay private to the direction.
	for _, path := range []string{
		prefix + "/reviews",
		prefix + "/board",
		prefix + "/intents",
		prefix + "/messages",
		prefix + "/runs",
		prefix + "/runs/" + run.ID + "/output",
		prefix + "/runs/" + run.ID + "/context",
		prefix + "/runs/" + supportRun.ID + "/events",
		prefix + "/goals",
	} {
		if code, body := do(http.MethodGet, token, path); code != http.StatusNotFound {
			t.Errorf("review GET %s = %d %q, want 404", path, code, body)
		}
	}

	// Any write is absent too, including the review trigger for the very fact
	// this run was started to judge.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, prefix + "/facts/" + f.ID + "/verify"},
		{http.MethodPost, prefix + "/intents"},
		{http.MethodPost, prefix + "/messages"},
		{http.MethodPut, prefix + "/state"},
	} {
		if code, body := do(c.method, token, c.path); code != http.StatusNotFound {
			t.Errorf("review %s %s = %d %q, want 404", c.method, c.path, code, body)
		}
	}

	// The credential is bound to its project: another project's identical path
	// is as absent as a route it may not see, and an instance-level path is not
	// part of the surface at all.
	if code, body := do(http.MethodGet, token, "/p/p-other/api/facts"); code != http.StatusNotFound {
		t.Errorf("review GET another project = %d %q, want 404", code, body)
	}
	if code, body := do(http.MethodGet, token, "/api/facts"); code != http.StatusNotFound {
		t.Errorf("review GET instance-level facts = %d %q, want 404", code, body)
	}

	// The participant surface is the other entrance, and it resolves session
	// identities only: a read-only credential is not one, so the executor's write
	// surface stays shut even before the review gate is consulted.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/agent/" + p.ID + "/api/messages"},
		{http.MethodGet, "/agent/" + p.ID + "/api/facts"},
	} {
		if code, body := do(c.method, token, c.path); code != http.StatusUnauthorized {
			t.Errorf("review %s %s = %d %q, want 401", c.method, c.path, code, body)
		}
	}

	// It dies with the run. A settled review keeps no standing credential.
	registry.RevokeRun("r_verify")
	if code, body := do(http.MethodGet, token, prefix+"/facts"); code != http.StatusUnauthorized {
		t.Errorf("revoked review credential = %d %q, want 401", code, body)
	}
}
