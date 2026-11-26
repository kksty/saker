package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The proposal surface is the only structured way an executor can ask for the
// next direction: it can file/list/read/withdraw its own suggestions, and it can
// never decide them. The commander and the operator decide on the project face;
// an approved proposal still needs an explicit dispatch before a run starts.

func proposalTestID(participant string) collab.Identity {
	intent := ""
	if id, ok := collab.IntentIDFromParticipant(participant); ok {
		intent = id
	}
	return collab.Identity{ProjectID: "default", ParticipantID: participant, IntentID: intent, RunID: "r_" + participant, Generation: 1}
}

// proposalTestRequest builds one request with the context markers the real auth
// gate attaches: a participant identity for /agent and project-face reads, and
// the agent-surface marker for the (identity-less) commander write path.
func proposalTestRequest(method, path string, body any, id *collab.Identity, commander bool) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			panic(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	ctx := req.Context()
	if id != nil {
		ctx = WithIdentity(ctx, *id)
	}
	if commander {
		ctx = WithAgentSurface(ctx)
		req.Header.Set("X-Tw-Actor", collab.CommanderID)
	}
	return req.WithContext(ctx)
}

func proposalTestServe(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func proposalTestProject(s *Server, t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	return proposalTestServe(t, requestMux(s), req)
}

func proposalTestBody(key, title string) map[string]any {
	return map[string]any{
		"title":           title,
		"description":     "确认 /api/admin 能否在未登录下读取，并保存最小复现",
		"eval":            "能复现一次未授权读取并留住响应原文",
		"rationale":       "上一轮已确认管理端可达",
		"idempotency_key": key,
	}
}

type proposalTestResponse struct {
	Proposal     model.Proposal `json:"proposal"`
	IntentID     string         `json:"intent_id"`
	RunID        string         `json:"run_id"`
	Created      bool           `json:"created"`
	Notification struct {
		Notified  bool   `json:"notified"`
		MessageID string `json:"message_id"`
		Error     string `json:"error"`
	} `json:"notification"`
	Error string `json:"error"`
	Code  string `json:"code"`
}

func proposalTestDecode(t *testing.T, rec *httptest.ResponseRecorder) proposalTestResponse {
	t.Helper()
	var out proposalTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %d %s: %v", rec.Code, rec.Body.String(), err)
	}
	return out
}

func proposalTestMessages(st *store.Store, to, kind string) []model.Message {
	var out []model.Message
	for _, m := range st.Messages(200) {
		if m.ToParticipantID == to && m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

func proposalTestFile(t *testing.T, s *Server, proposer collab.Identity, key, title string) model.Proposal {
	t.Helper()
	rec := proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodPost, "/api/proposals", proposalTestBody(key, title), &proposer, false))
	if rec.Code != http.StatusCreated {
		t.Fatalf("file proposal = %d %s", rec.Code, rec.Body.String())
	}
	return proposalTestDecode(t, rec).Proposal
}

func TestProposalExecutorFilesAndReadsOnlyOwn(t *testing.T) {
	s, _, st := newCommanderServer(t)
	mine := proposalTestID(collab.ParticipantID("in_a"))
	other := proposalTestID(collab.ParticipantID("in_b"))

	rec := proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodPost, "/api/proposals",
		proposalTestBody("k1", "验证管理端未授权读取"), &mine, false))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	out := proposalTestDecode(t, rec)
	p := out.Proposal
	if !out.Created || !out.Notification.Notified || out.Notification.MessageID == "" {
		t.Fatalf("create response = %+v", out)
	}
	if p.Status != model.ProposalProposed || p.Version != 1 || p.FromParticipantID != mine.ParticipantID ||
		p.FromIntentID != "in_a" || p.FromRunID != mine.RunID {
		t.Fatalf("created proposal = %+v", p)
	}
	if msgs := proposalTestMessages(st, collab.CommanderID, "proposal"); len(msgs) != 1 {
		t.Fatalf("commander notifications = %+v", msgs)
	}

	// The proposer sees its own suggestion.
	var list struct {
		Proposals []model.Proposal `json:"proposals"`
		Total     int              `json:"total"`
	}
	rec = proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodGet, "/api/proposals", nil, &mine, false))
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK || list.Total != 1 || list.Proposals[0].ID != p.ID {
		t.Fatalf("proposer list = %d %+v (%v)", rec.Code, list, err)
	}
	// Another executor must not see it, nor read it by id.
	rec = proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodGet, "/api/proposals", nil, &other, false))
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Total != 0 || len(list.Proposals) != 0 {
		t.Fatalf("peer list = %+v (%v)", list, err)
	}
	rec = proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodGet, "/api/proposals/"+p.ID, nil, &other, false))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("peer get = %d %s, want 404", rec.Code, rec.Body.String())
	}
	rec = proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodGet, "/api/proposals/"+p.ID, nil, &mine, false))
	if rec.Code != http.StatusOK {
		t.Fatalf("own get = %d %s", rec.Code, rec.Body.String())
	}
	// The operator (project face, no participant identity) sees the queue.
	rec = proposalTestProject(s, t, proposalTestRequest(http.MethodGet, "/api/proposals", nil, nil, false))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator list = %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Total != 1 {
		t.Fatalf("operator list = %+v (%v)", list, err)
	}
	// A participant identity is mandatory: a proposal is not an operator memo.
	rec = proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals", proposalTestBody("k2", "操作员替执行者提议"), nil, false))
	if rec.Code != http.StatusForbidden || proposalTestDecode(t, rec).Code != "proposal_forbidden" {
		t.Fatalf("operator create = %d %s", rec.Code, rec.Body.String())
	}

	// Retrying the same request key is one suggestion, one notification.
	rec = proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodPost, "/api/proposals",
		proposalTestBody("k1", "验证管理端未授权读取"), &mine, false))
	if rec.Code != http.StatusCreated {
		t.Fatalf("idempotent retry = %d %s", rec.Code, rec.Body.String())
	}
	out = proposalTestDecode(t, rec)
	if out.Created || out.Proposal.ID != p.ID {
		t.Fatalf("retry = %+v", out)
	}
	if n := len(st.Proposals()); n != 1 {
		t.Fatalf("proposals after retry = %d", n)
	}
	if msgs := proposalTestMessages(st, collab.CommanderID, "proposal"); len(msgs) != 1 {
		t.Fatalf("notification duplicated on retry: %+v", msgs)
	}
}

func TestProposalCreateValidation(t *testing.T) {
	s, _, st := newCommanderServer(t)
	mine := proposalTestID(collab.ParticipantID("in_a"))
	fact, err := st.AddFact(model.Fact{Kind: "note", Title: "只能作参考"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing key", map[string]any{"title": "x"}},
		{"missing work", map[string]any{"idempotency_key": "k"}},
		// A note is never a premise; the same rule as POST /api/intents.
		{"non-reviewable premise", map[string]any{"title": "x", "idempotency_key": "k", "from_fact_ids": []string{fact.ID}}},
		{"ghost context fact", map[string]any{"title": "x", "idempotency_key": "k", "context_fact_ids": []string{"f_ghost"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodPost, "/api/proposals", tc.body, &mine, false))
			if rec.Code != http.StatusBadRequest || proposalTestDecode(t, rec).Code != "proposal_invalid" {
				t.Fatalf("%s = %d %s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
	if n := len(st.Proposals()); n != 0 {
		t.Fatalf("invalid proposals were stored: %d", n)
	}
}

func TestProposalExecutorCannotDecide(t *testing.T) {
	s, _, _ := newCommanderServer(t)
	mine := proposalTestID(collab.ParticipantID("in_a"))
	p := proposalTestFile(t, s, mine, "k1", "验证管理端未授权读取")
	for _, action := range []string{"approve", "reject", "dispatch"} {
		body := map[string]any{"expected_version": p.Version, "note": "n", "reason": "r"}
		rec := proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/"+action, body, &mine, false))
		if rec.Code != http.StatusForbidden || proposalTestDecode(t, rec).Code != "proposal_forbidden" {
			t.Fatalf("executor %s = %d %s", action, rec.Code, rec.Body.String())
		}
	}
	// The decision routes are absent from the participant face entirely.
	rec := proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/approve",
		map[string]any{"expected_version": p.Version}, &mine, false))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("agent-face approve = %d, want 404", rec.Code)
	}
}

func TestProposalOperatorAndCommanderDecide(t *testing.T) {
	s, _, st := newCommanderServer(t)
	mine := proposalTestID(collab.ParticipantID("in_a"))

	// Rejection requires a reason and the version the caller read.
	first := proposalTestFile(t, s, mine, "k1", "验证管理端未授权读取")
	rec := proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+first.ID+"/reject",
		map[string]any{"expected_version": first.Version}, nil, false))
	if rec.Code != http.StatusBadRequest || proposalTestDecode(t, rec).Code != "proposal_invalid" {
		t.Fatalf("reject without reason = %d %s", rec.Code, rec.Body.String())
	}
	rec = proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+first.ID+"/reject",
		map[string]any{"expected_version": 9, "reason": "不做"}, nil, false))
	if rec.Code != http.StatusConflict || proposalTestDecode(t, rec).Code != "proposal_conflict" {
		t.Fatalf("stale reject = %d %s", rec.Code, rec.Body.String())
	}
	rec = proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+first.ID+"/reject",
		map[string]any{"expected_version": first.Version, "reason": "与本轮授权重叠，先做完原意图"}, nil, false))
	if rec.Code != http.StatusOK {
		t.Fatalf("reject = %d %s", rec.Code, rec.Body.String())
	}
	out := proposalTestDecode(t, rec)
	if out.Proposal.Status != model.ProposalRejected || out.Proposal.DecidedBy != collab.OperatorID || !out.Notification.Notified {
		t.Fatalf("reject response = %+v", out)
	}
	msgs := proposalTestMessages(st, mine.ParticipantID, "proposal_rejected")
	if len(msgs) != 1 || msgs[0].FromParticipantID != collab.OperatorID {
		t.Fatalf("rejection notification = %+v", msgs)
	}
	rec = proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+first.ID+"/approve",
		map[string]any{"expected_version": out.Proposal.Version}, nil, false))
	if rec.Code != http.StatusConflict {
		t.Fatalf("approve rejected = %d %s", rec.Code, rec.Body.String())
	}

	// The commander decides through the project face; approval creates a pending
	// direction and must not start it.
	second := proposalTestFile(t, s, mine, "k2", "复核管理端可达性")
	rec = proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+second.ID+"/approve",
		map[string]any{"expected_version": second.Version, "note": "值得做"}, nil, true))
	if rec.Code != http.StatusOK {
		t.Fatalf("commander approve = %d %s", rec.Code, rec.Body.String())
	}
	out = proposalTestDecode(t, rec)
	if out.Proposal.Status != model.ProposalApproved || out.Proposal.DecidedBy != collab.CommanderID || out.IntentID == "" || !out.Notification.Notified {
		t.Fatalf("approve response = %+v", out)
	}
	if out.Proposal.IntentID != out.IntentID {
		t.Fatalf("proposal.intent_id = %q, response intent_id = %q", out.Proposal.IntentID, out.IntentID)
	}
	if intent, ok := st.GetIntent(out.IntentID); !ok || intent.Status != model.StatusPending || intent.CreatedBy != collab.CommanderID {
		t.Fatalf("approved intent = %+v ok=%v", intent, ok)
	}
	if runs := st.Runs(); len(runs) != 0 {
		t.Fatalf("approval started a run: %+v", runs)
	}
	msgs = proposalTestMessages(st, mine.ParticipantID, "proposal_approved")
	if len(msgs) != 1 || msgs[0].FromParticipantID != collab.CommanderID {
		t.Fatalf("approval notification = %+v", msgs)
	}
}

func TestProposalDispatchStartsExactlyOneRun(t *testing.T) {
	s, _, st := newCommanderServer(t)
	mine := proposalTestID(collab.ParticipantID("in_a"))
	p := proposalTestFile(t, s, mine, "k1", "验证管理端未授权读取")
	rec := proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/approve",
		map[string]any{"expected_version": p.Version, "note": "值得做"}, nil, false))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body.String())
	}
	approved := proposalTestDecode(t, rec).Proposal
	if runs := st.Runs(); len(runs) != 0 {
		t.Fatalf("approve dispatched: %+v", runs)
	}

	rec = proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/dispatch",
		map[string]any{"expected_version": approved.Version}, nil, false))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("dispatch = %d %s", rec.Code, rec.Body.String())
	}
	out := proposalTestDecode(t, rec)
	if out.RunID == "" || out.Proposal.Status != model.ProposalDispatched || out.Proposal.RunID != out.RunID || !out.Notification.Notified {
		t.Fatalf("dispatch response = %+v", out)
	}
	if runs := st.Runs(); len(runs) != 1 || runs[0].ID != out.RunID || runs[0].IntentID != approved.IntentID {
		t.Fatalf("runs after dispatch = %+v", runs)
	}
	if msgs := proposalTestMessages(st, mine.ParticipantID, "proposal_dispatched"); len(msgs) != 1 {
		t.Fatalf("dispatch notification = %+v", msgs)
	}

	// A retry must report the existing dispatch instead of starting a second
	// run, even with the version observed before the dispatch.
	rec = proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/dispatch",
		map[string]any{"expected_version": approved.Version}, nil, false))
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch retry = %d %s", rec.Code, rec.Body.String())
	}
	retry := proposalTestDecode(t, rec)
	if retry.RunID != out.RunID || retry.Proposal.RunID != out.RunID {
		t.Fatalf("dispatch retry = %+v, want run %s", retry, out.RunID)
	}
	if runs := st.Runs(); len(runs) != 1 {
		t.Fatalf("retry started a second run: %+v", runs)
	}
	// Let the stub executor settle so the test leaves no live goroutine work.
	waitFor(t, 3*time.Second, func() bool {
		run, ok := st.GetRun(out.RunID)
		return ok && run.Status != model.RunRunning
	})
}

// Two dispatch requests arriving together must still start one run: the first
// records its receipt before releasing the project's scheduling lock, so the
// second re-reads "dispatched" and reports the same run instead of starting a
// second one (the intent claim alone is not enough once a fast run has failed).
func TestProposalConcurrentDispatchStartsOneRun(t *testing.T) {
	s, _, st := newCommanderServer(t)
	mine := proposalTestID(collab.ParticipantID("in_a"))
	p := proposalTestFile(t, s, mine, "k1", "验证管理端未授权读取")
	rec := proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/approve",
		map[string]any{"expected_version": p.Version}, nil, false))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body.String())
	}
	approved := proposalTestDecode(t, rec).Proposal
	mux := requestMux(s)
	start := make(chan struct{})
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	runIDs := make(chan string, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/dispatch",
				map[string]any{"expected_version": approved.Version}, nil, false)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)
			codes <- response.Code
			var out proposalTestResponse
			_ = json.Unmarshal(response.Body.Bytes(), &out)
			runIDs <- out.RunID
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	close(runIDs)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[http.StatusAccepted] != 1 || counts[http.StatusOK] != 1 {
		t.Fatalf("concurrent dispatch codes = %+v, want one 202 and one 200", counts)
	}
	ids := map[string]bool{}
	for id := range runIDs {
		if id != "" {
			ids[id] = true
		}
	}
	if len(ids) != 1 {
		t.Fatalf("concurrent dispatch returned runs %v, want one", ids)
	}
	if runs := st.Runs(); len(runs) != 1 {
		t.Fatalf("concurrent dispatch started %d runs, want 1", len(runs))
	}
	runs := st.Runs()
	waitFor(t, 3*time.Second, func() bool {
		run, ok := st.GetRun(runs[0].ID)
		return ok && run.Status != model.RunRunning
	})
}

func TestProposalWithdrawIsProposerOnly(t *testing.T) {
	s, _, st := newCommanderServer(t)
	mine := proposalTestID(collab.ParticipantID("in_a"))
	other := proposalTestID(collab.ParticipantID("in_b"))
	p := proposalTestFile(t, s, mine, "k1", "验证管理端未授权读取")

	rec := proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/withdraw",
		map[string]any{"expected_version": p.Version}, &other, false))
	if rec.Code != http.StatusForbidden || proposalTestDecode(t, rec).Code != "proposal_forbidden" {
		t.Fatalf("peer withdraw = %d %s", rec.Code, rec.Body.String())
	}
	rec = proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/withdraw",
		map[string]any{"expected_version": p.Version}, nil, false))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("operator withdraw = %d %s, want 403", rec.Code, rec.Body.String())
	}
	rec = proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/withdraw",
		map[string]any{"expected_version": p.Version + 1}, &mine, false))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale withdraw = %d %s", rec.Code, rec.Body.String())
	}
	rec = proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/withdraw",
		map[string]any{"expected_version": p.Version}, &mine, false))
	if rec.Code != http.StatusOK {
		t.Fatalf("withdraw = %d %s", rec.Code, rec.Body.String())
	}
	out := proposalTestDecode(t, rec)
	if out.Proposal.Status != model.ProposalWithdrawn || !out.Notification.Notified {
		t.Fatalf("withdraw response = %+v", out)
	}
	if msgs := proposalTestMessages(st, collab.CommanderID, "proposal_withdrawn"); len(msgs) != 1 {
		t.Fatalf("withdraw notification = %+v", msgs)
	}
	// A withdrawn suggestion cannot be approved afterwards.
	rec = proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/approve",
		map[string]any{"expected_version": out.Proposal.Version}, nil, false))
	if rec.Code != http.StatusConflict {
		t.Fatalf("approve withdrawn = %d %s", rec.Code, rec.Body.String())
	}
}

// Approval re-checks the premises: a fact can be withdrawn between the proposal
// and the decision, and an approved direction must not cite an unusable premise.
func TestProposalApproveRechecksPremises(t *testing.T) {
	s, _, st := newCommanderServer(t)
	mine := proposalTestID(collab.ParticipantID("in_a"))
	fact, err := st.AddFact(model.Fact{Kind: "vuln", Title: "管理端可达"})
	if err != nil {
		t.Fatal(err)
	}
	body := proposalTestBody("k1", "验证管理端未授权读取")
	body["from_fact_ids"] = []string{fact.ID}
	rec := proposalTestServe(t, s.AgentHandler(), proposalTestRequest(http.MethodPost, "/api/proposals", body, &mine, false))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	p := proposalTestDecode(t, rec).Proposal
	if _, err := st.WithdrawFact(fact.ID, collab.OperatorID, "", "误报，撤回"); err != nil {
		t.Fatal(err)
	}
	rec = proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/"+p.ID+"/approve",
		map[string]any{"expected_version": p.Version}, nil, false))
	if rec.Code != http.StatusConflict || proposalTestDecode(t, rec).Code != "proposal_premise" {
		t.Fatalf("approve with withdrawn premise = %d %s", rec.Code, rec.Body.String())
	}
	got, _ := st.GetProposal(p.ID)
	if got.Status != model.ProposalProposed || got.Version != p.Version {
		t.Fatalf("proposal changed after refused approval: %+v", got)
	}
}

func TestProposalNotFound(t *testing.T) {
	s, _, _ := newCommanderServer(t)
	rec := proposalTestProject(s, t, proposalTestRequest(http.MethodGet, "/api/proposals/pr_missing", nil, nil, false))
	if rec.Code != http.StatusNotFound || proposalTestDecode(t, rec).Code != "proposal_not_found" {
		t.Fatalf("get missing = %d %s", rec.Code, rec.Body.String())
	}
	rec = proposalTestProject(s, t, proposalTestRequest(http.MethodPost, "/api/proposals/pr_missing/approve",
		map[string]any{"expected_version": 1}, nil, false))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("approve missing = %d %s", rec.Code, rec.Body.String())
	}
}

// Active pagination must include older work even when a newer page is all history.
func TestProposalQueueListsActiveAndHistoricalPagesIndependently(t *testing.T) {
	s, _ := newBindingServer(t)
	for _, id := range []string{"old-active", "new-active", "newer-closed"} {
		p, _, err := s.Store.CreateProposal(model.Proposal{
			ID: id, IdempotencyKey: id, Title: id, FromParticipantID: "test-participant",
		})
		if err != nil {
			t.Fatal(err)
		}
		if id == "newer-closed" {
			if _, _, err := s.Store.DecideProposal(p.ID, p.Version, false, "synthetic decision", "operator"); err != nil {
				t.Fatal(err)
			}
		}
	}
	read := func(path string) (ids []string, next string, total int) {
		t.Helper()
		rec := call(t, s, http.MethodGet, path, nil)
		var page struct {
			Rows  []model.Proposal `json:"proposals"`
			Next  string           `json:"next_cursor"`
			Total int              `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s (%v)", path, rec.Code, rec.Body.String(), err)
		}
		for _, p := range page.Rows {
			ids = append(ids, p.ID)
		}
		return ids, page.Next, page.Total
	}
	if rows, _, total := read("/api/proposals?status=closed"); total != 1 || len(rows) != 1 || rows[0] != "newer-closed" {
		t.Fatalf("unexpected history: %v (%d)", rows, total)
	}
	rows, cursor, total := read("/api/proposals?status=active&limit=1")
	if total != 2 || len(rows) != 1 || cursor == "" {
		t.Fatalf("unexpected first page: %v (%d), cursor=%q", rows, total, cursor)
	}
	last, next, total := read("/api/proposals?status=active&limit=1&cursor=" + cursor)
	if total != 2 || len(last) != 1 || rows[0] == last[0] || next != "" {
		t.Fatalf("unexpected second page: %v (%d), cursor=%q", last, total, next)
	}
}
