package main

// Tests for the native read verbs (cli.go). A local httptest server stands in
// for the project API; assertions cover formatting, field clipping, ANSI/
// newline cleanup, cursor passthrough, byte caps, exit codes, unknown verbs and
// the one-round wake contract.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/kksty/saker/internal/testutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/retrieval"
	"github.com/kksty/saker/internal/store"
)

const (
	cliTestPathPrefix = "/p/p1/api"
	cliTestToken      = "tok-123"
)

func cliTestPath(p string) string { return cliTestPathPrefix + p }

type cliTestRequest struct {
	path  string
	query string
	auth  string
}

// cliTestServer serves fixed bodies per path and records every request, so a
// test can assert both the output and the number of network reads.
type cliTestServer struct {
	mu       sync.Mutex
	routes   map[string]string
	status   map[string]int
	requests []cliTestRequest
}

func newCLITestServer(t *testing.T, routes map[string]string) (*cliTestServer, *httptest.Server) {
	t.Helper()
	ts := &cliTestServer{routes: routes, status: map[string]int{}}
	srv := testutil.NewServer(t, ts)
	t.Cleanup(srv.Close)
	return ts, srv
}

func (s *cliTestServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, cliTestRequest{
		path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization"),
	})
	body, ok := s.routes[r.URL.Path]
	code := s.status[r.URL.Path]
	s.mu.Unlock()
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if code == 0 {
		code = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprint(w, body)
}

func (s *cliTestServer) snapshot() []cliTestRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]cliTestRequest(nil), s.requests...)
}

func (s *cliTestServer) setStatus(path string, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[path] = code
}

// cliTestEnv is the injected environment: the real session shell exports these
// three, and SAKER_AUTH is the ready-made Authorization line from .saker/auth.
func cliTestEnv(base string) func(string) string {
	return func(key string) string {
		switch key {
		case "SAKER_BATTLE":
			return base
		case "SAKER_AUTH":
			return "Authorization: Bearer " + cliTestToken
		case "SAKER_PROJECT":
			return "p1"
		case "SAKER_SELF":
			return "in_1"
		}
		return ""
	}
}

func runCLITest(t *testing.T, srv *httptest.Server, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runCLITestEnv(t, srv, cliTestEnv(srv.URL+cliTestPathPrefix), args...)
}

func runCLITestEnv(t *testing.T, srv *httptest.Server, env func(string) string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = runCLIWithEnv(args, &out, &errOut, env)
	return out.String(), errOut.String(), code
}

// cliFieldValue returns one key=value field from a tab-separated record line.
func cliFieldValue(line, key string) string {
	for _, field := range strings.Split(line, "\t") {
		if v, ok := strings.CutPrefix(field, key+"="); ok {
			return v
		}
	}
	return ""
}

func cliLines(out string) []string { return strings.Split(strings.TrimSuffix(out, "\n"), "\n") }

func cliFindLine(t *testing.T, out, prefix string) string {
	t.Helper()
	for _, line := range cliLines(out) {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("no line starts with %q in:\n%s", prefix, out)
	return ""
}

// cliAssertTrailer asserts the three-line trailer every read verb ends with.
func cliAssertTrailer(t *testing.T, out string, hasMore bool, cursor string) {
	t.Helper()
	want := fmt.Sprintf("has_more=%t\nnext_cursor=%s\n", hasMore, cursor)
	if !strings.HasSuffix(out, want) {
		t.Fatalf("output does not end with %q:\n%s", want, out)
	}
	if !strings.Contains(out, "truncated=") {
		t.Fatalf("output has no truncated= trailer:\n%s", out)
	}
}

// cliAssertModelReadable asserts the output contract: no ANSI, no raw JSON
// punctuation (one line per record), and no credential echo.
func cliAssertModelReadable(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("output contains ANSI escapes: %q", out)
	}
	if strings.Contains(out, "{") || strings.Contains(out, "}") {
		t.Fatalf("output looks like raw JSON: %q", out)
	}
	if strings.Contains(out, cliTestToken) {
		t.Fatalf("output leaked the token: %q", out)
	}
}

// ---- fixtures ----

func cliTestTime() time.Time {
	return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
}

func cliBoardFixture(t *testing.T) string {
	t.Helper()
	longTitle := strings.Repeat("A", 100)
	fixture := struct {
		board.View
		ProjectPaused    bool   `json:"project_paused"`
		ProjectPauseKind string `json:"project_pause_kind,omitempty"`
	}{
		View: board.View{
			Intents:  board.Counts{Total: 4, Pending: 2, Held: 1, Running: 1, Done: 1},
			Facts:    board.FactStats{Total: 3, WithEvidence: 2, Verified: 1, Unverified: 2},
			Evidence: 2,
			Runs:     board.RunCounts{Total: 2, Running: 1, Done: 1},
			Goals:    board.GoalStats{Total: 1, Open: []board.GoalItem{{ID: "g_1", Statement: "拿下管理员"}}},
			Open: []board.Item{{
				ID: "in_1", Title: longTitle, Status: "pending", Attempts: 1, CreatedAt: cliTestTime(),
			}},
			Held: []board.Item{{ID: "in_2", Title: "\x1b[31m停放\x1b[0m意图", Status: "pending", Attempts: 2, CreatedAt: cliTestTime()}},
			Running: []board.Item{{
				ID: "in_3", Title: "在跑意图", Status: "running", Attempts: 1, CreatedAt: cliTestTime(),
				Pulse: &board.RunPulse{Run: "r_1", IdleSeconds: 42, Events: 17, DeclaredFacts: 1, LastKind: "tool_call", LastTool: "bash", LastText: "nmap -sV 10.0.0.5"},
			}},
			RecentFacts: []board.Item{{
				ID: "f_1", Title: "ThinkPHP RCE", Summary: "命令执行", Status: "verified",
				Kind: "vuln", Origin: "declared", Evidence: 2, IntentID: "in_1", CreatedAt: cliTestTime(),
			}},
			Playbooks: board.PlaybookStats{},
		},
		ProjectPaused: false,
	}
	b, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cliFocusFixture(t *testing.T) string {
	t.Helper()
	fixture := retrieval.FocusPage{
		Intent: "in_1",
		Layer:  "hot",
		Hot:    1,
		Cold:   2,
		Facts: []retrieval.FocusFact{{
			ID: "f_1", Kind: "vuln", Title: "ThinkPHP RCE", Summary: "入口已确认",
			Status: "verified", Temperature: "hot", Reasons: []string{"required", "pinned"},
			ClaimVersion: 2, Query: "/api/facts/f_1",
		}},
		PageInfo: retrieval.PageInfo{HasMore: true, NextCursor: "cur-focus-1", Total: 3},
	}
	b, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cliFactsFixture(t *testing.T) string {
	t.Helper()
	fixture := retrieval.FactPage{
		Facts: []model.Fact{{
			ID: "f_1", Kind: "vuln", IntentID: "in_1", Title: "ThinkPHP RCE",
			Summary: "命令执行", Confidence: "observed", Origin: "declared",
			Status: "unverified", EvidenceIDs: []string{"e_1"}, CreatedAt: cliTestTime(),
		}},
		PageInfo: retrieval.PageInfo{HasMore: true, NextCursor: "cur-facts-1", Total: 7},
	}
	b, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cliFactFixture(id, title string) string {
	return fmt.Sprintf(`{"fact":{"id":%q,"kind":"vuln","intent_id":"in_1","title":%q,"summary":"s","confidence":"observed","origin":"declared","status":"verified","evidence_ids":["e_1"]},"version":{"epoch":"ep","seq":1},"reviews":[{"id":"rv_1","fact_id":%q,"verdict":"pass","claim_version":2,"verifier_run":"r_9","notes":"重跑一致","created_at":"2026-01-02T03:04:05Z"}]}`, id, title, id)
}

func cliChangesFixture(t *testing.T) string {
	t.Helper()
	fixture := retrieval.ChangePage{
		Changes: []store.Change{{
			Seq: 3, Transaction: "tx", Kind: "fact", ID: "f_1", Version: 2,
			Operation: "update", At: cliTestTime(),
		}},
		NextCursor:    "cur-chg-1",
		HasMore:       true,
		HighWatermark: store.ReadVersion{Epoch: "ep", Seq: 5},
		Pending:       2,
	}
	b, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cliMessagesFixture(t *testing.T) string {
	t.Helper()
	fixture := struct {
		Messages []model.Message `json:"messages"`
	}{Messages: []model.Message{{
		ID: "m_1", Seq: 7, ProjectID: "p1", FromParticipantID: "pt_in_1",
		ToParticipantID: "commander", Kind: "say", Text: "请确认日志写入点",
		Status: "queued", CreatedAt: cliTestTime(),
	}}}
	b, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const cliReadinessFixture = `{"integrity_checked":true,"intent_id":"in_1","policy":"verified_only","ready":false,` +
	`"premises":[{"id":"f_1","title":"前提：入口可达","claim_version":3,"status":"unverified","usable":false,` +
	`"reason":"缺少绑定当前断言的独立核验收据","integrity":"not_checked","review":{"state":"running"}}],` +
	`"review_hint":"前提复核：1 条正在独立复核，等下一轮唤醒。","self_reference_hint":""}`

func cliReviewsFixture(t *testing.T) string {
	t.Helper()
	fixture := struct {
		Reviews []model.Review `json:"reviews"`
	}{Reviews: []model.Review{{
		ID: "rv_1", FactID: "f_1", Verdict: "pass", Notes: "重跑一致",
		ClaimVersion: 2, ClaimHash: "h", VerifierRun: "r_9", CreatedAt: cliTestTime(),
	}}}
	b, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cliFullRoutes(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		cliTestPath("/board"):                  cliBoardFixture(t),
		cliTestPath("/focus"):                  cliFocusFixture(t),
		cliTestPath("/facts"):                  cliFactsFixture(t),
		cliTestPath("/facts/f_1"):              cliFactFixture("f_1", "单个事实一"),
		cliTestPath("/facts/f_2"):              cliFactFixture("f_2", "单个事实二"),
		cliTestPath("/changes"):                cliChangesFixture(t),
		cliTestPath("/messages"):               cliMessagesFixture(t),
		cliTestPath("/reviews"):                cliReviewsFixture(t),
		cliTestPath("/intents/in_1/readiness"): cliReadinessFixture,
	}
}

// ---- board ----

func TestCLIBoardFormatting(t *testing.T) {
	ts, srv := newCLITestServer(t, cliFullRoutes(t))
	out, errOut, code := runCLITest(t, srv, "board")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	cliAssertTrailer(t, out, false, "")
	cliAssertModelReadable(t, out)
	for _, want := range []string{
		"type=project\tpaused=false",
		"type=counts\tscope=intents\ttotal=4\tpending=2\theld=1",
		"type=counts\tscope=facts\ttotal=3\tverified=1",
		"type=counts\tscope=runs\ttotal=2\trunning=1",
		"type=counts\tscope=goals\ttotal=1\tmet=0\topen=1",
		"type=intent\tstatus=open\tid=in_1",
		"type=intent\tstatus=held\tid=in_2",
		"type=intent\tstatus=running\tid=in_3",
		"type=pulse\tintent=in_3\tidle_seconds=42\tevents=17",
		"type=fact\tid=f_1\tkind=vuln\tstatus=verified",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("board output missing %q:\n%s", want, out)
		}
	}
	// Every field is clipped to 80 runes inclusive of the ellipsis.
	openLine := cliFindLine(t, out, "type=intent\tstatus=open")
	if got := cliFieldValue(openLine, "title"); utf8.RuneCountInString(got) > cliFieldRunes {
		t.Fatalf("title field = %d runes, want <= %d: %q", utf8.RuneCountInString(got), cliFieldRunes, got)
	}
	// The ANSI sequence copied into a title is stripped but the text stays.
	heldLine := cliFindLine(t, out, "type=intent\tstatus=held")
	if !strings.Contains(heldLine, "停放意图") {
		t.Fatalf("cleaned title lost its text: %q", heldLine)
	}
	requests := ts.snapshot()
	if len(requests) != 1 || requests[0].path != cliTestPath("/board") {
		t.Fatalf("requests = %+v, want exactly one board read", requests)
	}
	if requests[0].auth != "Bearer "+cliTestToken {
		t.Fatalf("Authorization = %q", requests[0].auth)
	}
}

// ---- focus ----

func TestCLIFocusLayerAndCursor(t *testing.T) {
	ts, srv := newCLITestServer(t, cliFullRoutes(t))
	out, errOut, code := runCLITest(t, srv, "focus", "--intent", "in_1", "--layer", "hot")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	cliAssertTrailer(t, out, true, "cur-focus-1")
	cliAssertModelReadable(t, out)
	for _, want := range []string{
		"type=counts\tscope=focus\tintent=in_1\tlayer=hot\thot=1\twarm=0\tcold=2\ttotal=3",
		"type=fact\tscope=focus\tid=f_1\tkind=vuln\tstatus=verified\ttemperature=hot\treasons=required,pinned",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("focus output missing %q:\n%s", want, out)
		}
	}
	req := ts.snapshot()[0]
	if !strings.Contains(req.query, "intent=in_1") || !strings.Contains(req.query, "layer=hot") || !strings.Contains(req.query, "limit=10") {
		t.Fatalf("query = %q", req.query)
	}
	// SAKER_SELF fills in the intent when --intent is omitted.
	out, _, code = runCLITest(t, srv, "focus")
	if code != 0 || !strings.Contains(out, "intent=in_1") {
		t.Fatalf("SAKER_SELF fallback broken: code=%d out=%q", code, out)
	}
	// An unknown layer is a usage error, never a silent "all".
	_, errOut, code = runCLITest(t, srv, "focus", "--intent", "in_1", "--layer", "warmm")
	if code != 2 || !strings.Contains(errOut, "error=invalid_args") {
		t.Fatalf("bad layer: code=%d stderr=%q", code, errOut)
	}
}

// The focus cursor is not decoration: without this flag the cursor printed by
// saker focus (and by the wake focus section) could not be consumed at all.
func TestCLIFocusCursorAndLimit(t *testing.T) {
	ts, srv := newCLITestServer(t, cliFullRoutes(t))
	out, errOut, code := runCLITest(t, srv, "focus", "--intent", "in_1", "--cursor", "cur-focus-1", "--limit", "3")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "type=focus_page	intent=in_1	has_more=true	next_cursor=cur-focus-1") {
		t.Fatalf("focus page line missing:\n%s", out)
	}
	q := ts.snapshot()[0].query
	for _, want := range []string{"intent=in_1", "cursor=cur-focus-1", "limit=3"} {
		if !strings.Contains(q, want) {
			t.Fatalf("query %q missing %q", q, want)
		}
	}
}

// An executor reads its own mailbox, not the operator's whole-project message
// view; the after/box cursor is what makes an inbox read incremental.
func TestCLIInboxUsesAgentMailboxForADirection(t *testing.T) {
	base := "/agent/p1/api"
	messages := `{"messages":[{"id":"m_9","seq":9,"from_participant_id":"commander","to_participant_id":"pt_in_1","kind":"say","status":"queued","text":"go","created_at":"2026-01-02T03:04:05Z"}],"next":9}`
	_, srv := newCLITestServer(t, map[string]string{base + "/messages": messages})
	env := func(key string) string {
		switch key {
		case "SAKER_BATTLE":
			return srv.URL + cliTestPathPrefix
		case "SAKER_AGENT":
			return srv.URL + base
		case "SAKER_AUTH":
			return "Authorization: Bearer " + cliTestToken
		case "SAKER_PROJECT":
			return "p1"
		case "SAKER_SELF":
			return "in_1"
		}
		return ""
	}
	out, errOut, code := runCLITestEnv(t, srv, env, "inbox", "--after", "5", "--box", "inbox", "--limit", "3")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "type=inbox_page	scope=agent") || !strings.Contains(out, "next_cursor=9") {
		t.Fatalf("agent inbox page missing:\n%s", out)
	}
	if !strings.Contains(out, "type=message	id=m_9") || !strings.Contains(out, "text=go") {
		t.Fatalf("agent message missing:\n%s", out)
	}
}

// ---- facts ----

func TestCLIFactsQuery(t *testing.T) {
	ts, srv := newCLITestServer(t, cliFullRoutes(t))
	out, errOut, code := runCLITest(t, srv, "facts",
		"--q", "thinkphp", "--kind", "vuln", "--intent", "in_1", "--limit", "5")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	cliAssertTrailer(t, out, true, "cur-facts-1")
	cliAssertModelReadable(t, out)
	if !strings.Contains(out, "type=fact\tid=f_1\tkind=vuln\tstatus=unverified\tintent=in_1\torigin=declared\tconfidence=observed\tevidence=1") {
		t.Fatalf("facts output:\n%s", out)
	}
	req := ts.snapshot()[0]
	for _, want := range []string{"q=thinkphp", "kind=vuln", "intent=in_1", "limit=5"} {
		if !strings.Contains(req.query, want) {
			t.Fatalf("query %q missing %q", req.query, want)
		}
	}
}

func TestCLIFactsByIDs(t *testing.T) {
	ts, srv := newCLITestServer(t, cliFullRoutes(t))
	out, errOut, code := runCLITest(t, srv, "facts", "--ids", "f_1, f_2")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	cliAssertTrailer(t, out, false, "")
	cliAssertModelReadable(t, out)
	for _, want := range []string{"id=f_1", "id=f_2", "title=单个事实一", "title=单个事实二"} {
		if !strings.Contains(out, want) {
			t.Fatalf("facts --ids output missing %q:\n%s", want, out)
		}
	}
	requests := ts.snapshot()
	if len(requests) != 2 || requests[0].path != cliTestPath("/facts/f_1") || requests[1].path != cliTestPath("/facts/f_2") {
		t.Fatalf("requests = %+v, want one depth read per id", requests)
	}
}

// ---- changes ----

func TestCLIChangesCursor(t *testing.T) {
	ts, srv := newCLITestServer(t, cliFullRoutes(t))
	out, errOut, code := runCLITest(t, srv, "changes", "--since", "4", "--limit", "10")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	cliAssertTrailer(t, out, true, "cur-chg-1")
	cliAssertModelReadable(t, out)
	for _, want := range []string{
		"type=window\tepoch=ep\tseq=5\tpending=2",
		"type=change\tseq=3\tkind=fact\toperation=update\tid=f_1\tversion=2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("changes output missing %q:\n%s", want, out)
		}
	}
	req := ts.snapshot()[0]
	if !strings.Contains(req.query, "since=4") || !strings.Contains(req.query, "limit=10") {
		t.Fatalf("query = %q", req.query)
	}
	// The cursor is opaque: it is forwarded verbatim, never parsed.
	out, _, code = runCLITest(t, srv, "changes", "--cursor", "cur-chg-1")
	if code != 0 {
		t.Fatalf("cursor passthrough exit = %d", code)
	}
	if got := ts.snapshot()[1].query; !strings.Contains(got, "cursor=cur-chg-1") || !strings.Contains(got, "limit=10") {
		t.Fatalf("cursor query = %q", got)
	}
}

// ---- inbox ----

func TestCLIInbox(t *testing.T) {
	ts, srv := newCLITestServer(t, cliFullRoutes(t))
	out, errOut, code := runCLITest(t, srv, "inbox", "--limit", "3")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	cliAssertTrailer(t, out, false, "")
	cliAssertModelReadable(t, out)
	if !strings.Contains(out, "type=message\tid=m_1\tseq=7\tfrom=pt_in_1\tto=commander\tkind=say\tstatus=queued") {
		t.Fatalf("inbox output:\n%s", out)
	}
	if got := ts.snapshot()[0].query; got != "limit=3" {
		t.Fatalf("query = %q", got)
	}
}

// ---- readiness / reviews ----

func TestCLIReadiness(t *testing.T) {
	_, srv := newCLITestServer(t, cliFullRoutes(t))
	out, errOut, code := runCLITest(t, srv, "readiness", "--intent", "in_1")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	cliAssertTrailer(t, out, false, "")
	cliAssertModelReadable(t, out)
	for _, want := range []string{
		"type=readiness\tintent=in_1\tpolicy=verified_only\tready=false\tintegrity_checked=true",
		"type=premise\tid=f_1\tstatus=unverified\tusable=false\tintegrity=not_checked\tclaim_version=3\treview=running",
		"type=hint\ttext=前提复核：1 条正在独立复核，等下一轮唤醒。",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("readiness output missing %q:\n%s", want, out)
		}
	}
}

func TestCLIReviews(t *testing.T) {
	ts, srv := newCLITestServer(t, cliFullRoutes(t))
	out, errOut, code := runCLITest(t, srv, "reviews", "--fact", "f_1")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	cliAssertTrailer(t, out, false, "")
	cliAssertModelReadable(t, out)
	if !strings.Contains(out, "type=review\tid=rv_1\tfact=f_1\tverdict=pass\tclaim_version=2\tverifier_run=r_9") {
		t.Fatalf("reviews output:\n%s", out)
	}
	if got := ts.snapshot()[0].query; got != "fact=f_1" {
		t.Fatalf("query = %q", got)
	}
}

// A harness session's per-round credential cannot see the operator's
// /api/reviews surface (the gate answers 404), but the same verdict rows ride
// the fact depth read. A per-fact query must still answer from that surface.
func TestCLIReviewsFallsBackToFactDepth(t *testing.T) {
	ts, srv := newCLITestServer(t, cliFullRoutes(t))
	ts.setStatus(cliTestPath("/reviews"), http.StatusNotFound)
	out, errOut, code := runCLITest(t, srv, "reviews", "--fact", "f_1")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	if !strings.Contains(out, "type=review\tid=rv_1\tfact=f_1\tverdict=pass") {
		t.Fatalf("fallback reviews output:\n%s", out)
	}
	requests := ts.snapshot()
	if len(requests) != 2 || requests[0].path != cliTestPath("/reviews") || requests[1].path != cliTestPath("/facts/f_1") {
		t.Fatalf("requests = %+v, want reviews then fact depth", requests)
	}
}

// ---- errors ----

func TestCLIAPIError(t *testing.T) {
	ts, srv := newCLITestServer(t, cliFullRoutes(t))
	ts.setStatus(cliTestPath("/board"), http.StatusInternalServerError)
	out, errOut, code := runCLITest(t, srv, "board")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%q out=%q)", code, errOut, out)
	}
	if !strings.Contains(errOut, "error=500") {
		t.Fatalf("stderr = %q, want error=500 line", errOut)
	}
	if strings.Contains(errOut, cliTestToken) {
		t.Fatalf("error leaked the token: %q", errOut)
	}
}

func TestCLIConfigMissing(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runCLIWithEnv([]string{"board"}, &out, &errOut, func(string) string { return "" })
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "error=config") {
		t.Fatalf("stderr = %q, want error=config", errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
}

func TestCLIUnknownVerbReturnsFalse(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--addr", "127.0.0.1:0"},
		{"serve"},
		{"BOARD"},
		{"boardx"},
	} {
		if maybeRunCLI(args) {
			t.Fatalf("maybeRunCLI(%v) = true, want false", args)
		}
	}
}

func TestCLIInvalidArguments(t *testing.T) {
	_, srv := newCLITestServer(t, cliFullRoutes(t))
	for _, args := range [][]string{
		{"board", "--nope"},
		{"board", "extra"},
		{"facts", "--limit", "-1"},
		{"changes", "--since", "abc"},
		{"inbox", "--limit", "0"},
	} {
		_, errOut, code := runCLITest(t, srv, args...)
		if code != 2 || !strings.Contains(errOut, "error=invalid_args") {
			t.Fatalf("%v: code=%d stderr=%q", args, code, errOut)
		}
	}
}

// ---- wake ----

func TestCLIWakeSingleRoundOfRequests(t *testing.T) {
	ts, srv := newCLITestServer(t, cliFullRoutes(t))
	out, errOut, code := runCLITest(t, srv, "wake", "--intent", "in_1")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	if len(out) > cliWakeMaxBytes {
		t.Fatalf("wake output = %d bytes, want <= %d", len(out), cliWakeMaxBytes)
	}
	cliAssertTrailer(t, out, true, "cur-focus-1")
	cliAssertModelReadable(t, out)
	for _, section := range []string{"## board", "## focus", "## inbox", "## readiness"} {
		if !strings.Contains(out, section+"\n") {
			t.Fatalf("wake output missing section %q:\n%s", section, out)
		}
	}
	requests := ts.snapshot()
	if len(requests) != 4 {
		t.Fatalf("wake made %d requests (%+v), want exactly one round of 4", len(requests), requests)
	}
	seen := map[string]int{}
	for _, r := range requests {
		seen[r.path]++
	}
	for _, path := range []string{
		cliTestPath("/board"), cliTestPath("/focus"),
		cliTestPath("/messages"), cliTestPath("/intents/in_1/readiness"),
	} {
		if seen[path] != 1 {
			t.Fatalf("wake read %s %d times, want 1 (%+v)", path, seen[path], requests)
		}
	}

	// Without an intent, only board + inbox are readable: two requests, and the
	// two direction sections say why they are skipped instead of failing.
	ts2, srv2 := newCLITestServer(t, cliFullRoutes(t))
	base := srv2.URL + cliTestPathPrefix
	noSelf := func(key string) string {
		if key == "SAKER_SELF" {
			return ""
		}
		return cliTestEnv(base)(key)
	}
	out, _, code = runCLITestEnv(t, srv2, noSelf, "wake")
	if code != 0 {
		t.Fatalf("wake without intent exit = %d", code)
	}
	if n := len(ts2.snapshot()); n != 2 {
		t.Fatalf("wake without intent made %d requests, want 2", n)
	}
	if !strings.Contains(out, "跳过焦点") || !strings.Contains(out, "跳过准入") {
		t.Fatalf("wake without intent should explain skipped sections:\n%s", out)
	}
}

// ---- byte caps ----

func TestCLIOutputByteCaps(t *testing.T) {
	// A board with far more rows than 4 KiB can hold must be clipped, not
	// dumped: the model reads every byte of this output.
	view := board.View{
		Intents: board.Counts{Total: 300, Pending: 300},
		RecentFacts: func() []board.Item {
			items := make([]board.Item, 0, 200)
			for i := 0; i < 200; i++ {
				items = append(items, board.Item{
					ID: fmt.Sprintf("f_%d", i), Kind: "vuln", Status: "unverified",
					Origin: "declared", Title: strings.Repeat("B", 120),
				})
			}
			return items
		}(),
	}
	b, err := json.Marshal(struct {
		board.View
		ProjectPaused bool `json:"project_paused"`
	}{View: view})
	if err != nil {
		t.Fatal(err)
	}
	_, srv := newCLITestServer(t, map[string]string{cliTestPath("/board"): string(b)})
	out, errOut, code := runCLITest(t, srv, "board")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	if len(out) > cliMaxBytes {
		t.Fatalf("board output = %d bytes, want <= %d", len(out), cliMaxBytes)
	}
	cliAssertTrailer(t, out, true, "")
	if !strings.Contains(out, "truncated=true") || !strings.Contains(out, "dropped=") {
		t.Fatalf("clipped board does not say truncated/dropped:\n%s", out)
	}

	// The wake digest has its own, larger cap.
	_, wakeSrv := newCLITestServer(t, map[string]string{
		cliTestPath("/board"):                  string(b),
		cliTestPath("/messages"):               cliMessagesFixture(t),
		cliTestPath("/focus"):                  cliFocusFixture(t),
		cliTestPath("/intents/in_1/readiness"): cliReadinessFixture,
	})
	out, errOut, code = runCLITest(t, wakeSrv, "wake", "--intent", "in_1")
	if code != 0 {
		t.Fatalf("wake exit = %d, stderr = %s", code, errOut)
	}
	if len(out) > cliWakeMaxBytes {
		t.Fatalf("wake output = %d bytes, want <= %d", len(out), cliWakeMaxBytes)
	}
	if !strings.Contains(out, "truncated=true") {
		t.Fatalf("clipped wake does not say truncated=true:\n%s", out)
	}
}

// ---- field cleanup ----

// The session shell exports SAKER_BIN so a model can call the read verbs without
// guessing where the binary is; the script is regenerated when the path changes
// and carries no credential.
func TestCLISessionEnvExportsSakerBin(t *testing.T) {
	content := sessionEnvContent()
	if strings.Contains(content, sakerBinPlaceholder) {
		t.Fatalf("session env still carries the placeholder:\n%s", content)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	if abs, absErr := filepath.Abs(exe); absErr == nil {
		exe = abs
	}
	want := "SAKER_BIN=" + shellSingleQuote(filepath.ToSlash(exe)) + "\n"
	if !strings.Contains(content, want) {
		t.Fatalf("session env missing %q:\n%s", want, content)
	}
	if !strings.Contains(content, "export SAKER_BIN") {
		t.Fatalf("session env does not export SAKER_BIN:\n%s", content)
	}
	if strings.Contains(content, cliTestToken) {
		t.Fatalf("session env script embeds a credential")
	}
}

func TestCLIFieldCleanup(t *testing.T) {
	out := cliRecord("x", "a", "line1\nline2\tcol\r\n\x1b[31mred\x1b[0m", "b", strings.Repeat("界", 100))
	if strings.ContainsAny(out, "\n\r\x1b") {
		t.Fatalf("record still carries line/ANSI bytes: %q", out)
	}
	if !strings.Contains(out, "line1 line2 col") || !strings.Contains(out, "red") {
		t.Fatalf("record did not flatten/clean values: %q", out)
	}
	fields := strings.Split(out, "\t")
	last := fields[len(fields)-1]
	if !strings.HasPrefix(last, "b=") {
		t.Fatalf("last field = %q", last)
	}
	if got := utf8.RuneCountInString(strings.TrimPrefix(last, "b=")); got > cliFieldRunes {
		t.Fatalf("clipped field = %d runes, want <= %d", got, cliFieldRunes)
	}
	if cliParseToken("Authorization: Bearer abc") != "abc" || cliParseToken("Bearer abc") != "abc" || cliParseToken(" raw ") != "raw" {
		t.Fatal("cliParseToken shapes mismatch")
	}
}
