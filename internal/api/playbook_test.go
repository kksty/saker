package api

import (
	"encoding/json"
	"github.com/kksty/saker/internal/testutil"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// proofEvidence adds one real evidence row and returns its id. A confirmed label
// needs a source whose evidence the ledger can open: since the write gate refuses
// a pass on a citation with no row, a fixture that hard-codes "ev_1" would only
// prove the hole this rule closes.
func proofEvidence(t *testing.T, st *store.Store) string {
	t.Helper()
	ev, err := st.AddEvidence(model.Evidence{URI: "in_1/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	return ev.ID
}

// The console reads playbooks through their own endpoint, not /api/facts:
// confirmed entries lead, and retire is a state change, not a delete.
func TestListAndRetirePlaybooks(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A confirmed label needs sources that were verified and carry evidence;
	// otherwise the list (correctly) downgrades the writer's declaration.
	src := verifiedFixture(t, st, model.Fact{Kind: "vuln", Title: "已核验来源", EvidenceIDs: []string{proofEvidence(t, st)}})
	confirmed, _, err := st.UpsertPlaybook(model.Playbook{
		Title: "ThinkPHP RCE", When: "5.0.x 且日志可写", Doc: "curl -s /index.php?s=...",
		Tags: []string{"product:thinkphp"}, Origin: model.PlaybookFromExecutor, Status: model.PlaybookConfirmed,
		FromFactIDs: []string{src.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertPlaybook(model.Playbook{
		Title: "Redis 未授权", When: "6379 无密码",
		Origin: model.PlaybookFromCommander, Status: model.PlaybookCandidate, FromFactIDs: []string{"f_1"},
	}); err != nil {
		t.Fatal(err)
	}

	s := New(st, nil, t.TempDir(), t.TempDir())
	mux := http.NewServeMux()
	s.Register(mux)
	srv := testutil.NewServer(t, mux)
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/playbooks")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got struct {
		Count     int              `json:"count"`
		Playbooks []model.Playbook `json:"playbooks"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 2 || len(got.Playbooks) != 2 {
		t.Fatalf("list = %+v", got)
	}
	if got.Playbooks[0].ID != confirmed.ID || got.Playbooks[0].Status != model.PlaybookConfirmed {
		t.Fatalf("confirmed entry must lead: %+v", got.Playbooks)
	}

	resp, err := http.Post(srv.URL+"/api/playbooks/"+confirmed.ID+"/retire", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retire status = %d", resp.StatusCode)
	}
	p, ok := st.PlaybookByID(confirmed.ID)
	if !ok || p.Status != model.PlaybookRetired {
		t.Fatalf("retired row = %+v ok=%v", p, ok)
	}
}

// The board's playbook counters and the list's header counts describe the same
// derived label: a declared-confirmed row with an unqualified source cannot be
// confirmed on the board and a candidate in the list.
func TestBoardAndListAgreeOnDerivedPlaybookCounts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	good := verifiedFixture(t, st, model.Fact{Kind: "vuln", Title: "已核验来源", EvidenceIDs: []string{proofEvidence(t, st)}})
	bad, err := st.AddFact(model.Fact{Kind: "vuln", Title: "未核验来源"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertPlaybook(model.Playbook{Title: "合格打法", When: "w", Origin: model.PlaybookFromExecutor, Status: model.PlaybookConfirmed, FromFactIDs: []string{good.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertPlaybook(model.Playbook{Title: "来源未核验的打法", When: "w", Origin: model.PlaybookFromExecutor, Status: model.PlaybookConfirmed, FromFactIDs: []string{bad.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertPlaybook(model.Playbook{Title: "提名候选", When: "w", Origin: model.PlaybookFromCommander, Status: model.PlaybookCandidate}); err != nil {
		t.Fatal(err)
	}

	s := New(st, nil, t.TempDir(), t.TempDir())
	mux := http.NewServeMux()
	s.Register(mux)
	srv := testutil.NewServer(t, mux)
	defer srv.Close()

	var board struct {
		Playbooks struct {
			Confirmed  int `json:"confirmed"`
			Candidates int `json:"candidates"`
		} `json:"playbooks"`
	}
	res, err := http.Get(srv.URL + "/api/board")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&board); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Counts map[string]int `json:"counts"`
	}
	res2, err := http.Get(srv.URL + "/api/playbooks")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if err := json.NewDecoder(res2.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if board.Playbooks.Confirmed != 1 || board.Playbooks.Candidates != 2 {
		t.Fatalf("board counters = %+v, want 1 confirmed / 2 candidates", board.Playbooks)
	}
	if list.Counts[model.PlaybookConfirmed] != board.Playbooks.Confirmed || list.Counts[model.PlaybookCandidate] != board.Playbooks.Candidates {
		t.Fatalf("list counts %v disagree with the board %+v", list.Counts, board.Playbooks)
	}
}
