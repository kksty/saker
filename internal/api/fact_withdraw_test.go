package api

import (
	"encoding/json"
	"github.com/kksty/saker/internal/testutil"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

func newWithdrawServer(t *testing.T, kind string) (*Server, *httptest.Server, model.Fact) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{IntentID: "in_1", Kind: kind, Title: "9006 测试B1", Summary: "写入探针"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st}
	mux := http.NewServeMux()
	s.Register(mux)
	srv := testutil.NewServer(t, mux)
	t.Cleanup(srv.Close)
	return s, srv, f
}

func postWithdraw(t *testing.T, srv *httptest.Server, factID, body string) (int, map[string]any) {
	t.Helper()
	res, err := http.Post(srv.URL+"/api/facts/"+factID+"/withdraw", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestOperatorWithdrawsFactAndReasonIsRequired(t *testing.T) {
	s, srv, f := newWithdrawServer(t, "note")
	if code, _ := postWithdraw(t, srv, f.ID, `{}`); code != http.StatusBadRequest {
		t.Fatalf("missing reason = %d, want 400", code)
	}
	code, out := postWithdraw(t, srv, f.ID, `{"reason":"写入通道探针，不承载结论"}`)
	if code != http.StatusOK {
		t.Fatalf("withdraw = %d %+v", code, out)
	}
	got, _ := s.Store.FactByID(f.ID)
	if got.Status != "withdrawn" || got.RetractedBy != collab.OperatorID || got.RetractionReason == "" {
		t.Fatalf("withdrawn fact = %+v", got)
	}
	// Replay stays a success and keeps the first record.
	if code, _ := postWithdraw(t, srv, f.ID, `{"reason":"另一个理由"}`); code != http.StatusOK {
		t.Fatalf("replayed withdraw = %d, want 200", code)
	}
	if again, _ := s.Store.FactByID(f.ID); again.RetractionReason != got.RetractionReason {
		t.Fatalf("replay overwrote the reason: %q", again.RetractionReason)
	}
}

func TestExecutorWithdrawsOwnUnverifiedFactOnly(t *testing.T) {
	f := newAgentFixture(t)
	own, err := f.p.Store.AddFact(model.Fact{IntentID: f.src.ID, Kind: "note", Title: "9006 测试B1", Summary: "写入探针"})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.call(t, f.srcID, http.MethodPost, "/api/facts/"+own.ID+"/withdraw", map[string]any{"reason": "写入通道探针"})
	if rec.Code != http.StatusOK {
		t.Fatalf("own fact withdraw = %d %s", rec.Code, rec.Body.String())
	}
	got, _ := f.p.Store.FactByID(own.ID)
	if got.Status != "withdrawn" || got.RetractedBy != collab.ParticipantID(f.src.ID) || got.RetractedRunID != "r_src" {
		t.Fatalf("withdrawn fact = %+v", got)
	}

	// Another direction's fact is not the caller's to retract.
	foreign, _ := f.p.Store.AddFact(model.Fact{IntentID: f.target.ID, Kind: "note", Title: "别人的观察"})
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/facts/"+foreign.ID+"/withdraw", map[string]any{"reason": "想删"}); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign fact withdraw = %d, want 403", rec.Code)
	}

	// The commander does not own evidence.
	cmdID, err := f.srv.Collab.Issue(collab.Identity{ProjectID: f.p.ID, ParticipantID: collab.CommanderID, RunID: "r_cmd"})
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.call(t, cmdID, http.MethodPost, "/api/facts/"+foreign.ID+"/withdraw", map[string]any{"reason": "指挥官意见"}); rec.Code != http.StatusForbidden {
		t.Fatalf("commander withdraw = %d, want 403", rec.Code)
	}

	// An independently verified claim outranks its author; the operator keeps
	// the override.
	proof, err := f.p.Store.AddEvidence(model.Evidence{IntentID: f.src.ID, URI: f.src.ID + "/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	proven, _ := f.p.Store.AddFact(model.Fact{IntentID: f.src.ID, Kind: "vuln", Title: "已核验", EvidenceIDs: []string{proof.ID}})
	if _, err := f.p.Store.ReviewFact(model.Review{FactID: proven.ID, Verdict: "pass"}); err != nil {
		t.Fatal(err)
	}
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/facts/"+proven.ID+"/withdraw", map[string]any{"reason": "成绩不要了"}); rec.Code != http.StatusConflict {
		t.Fatalf("verified fact withdraw by author = %d, want 409", rec.Code)
	}
	// A rejected conclusion is a review outcome too: the author cannot erase it.
	rejected, _ := f.p.Store.AddFact(model.Fact{IntentID: f.src.ID, Kind: "vuln", Title: "被判不成立"})
	if _, err := f.p.Store.ReviewFact(model.Review{FactID: rejected.ID, Verdict: "reject", Notes: "复现不了"}); err != nil {
		t.Fatal(err)
	}
	if rec := f.call(t, f.srcID, http.MethodPost, "/api/facts/"+rejected.ID+"/withdraw", map[string]any{"reason": "不想留否定"}); rec.Code != http.StatusConflict {
		t.Fatalf("rejected fact withdraw by author = %d, want 409", rec.Code)
	}
}
