package main

// Native read-only status CLI for the control plane.
//
// The commander session used to read its own ledger with curl + python (each
// turn spent tokens and round-trips just to see the board). This file gives saker a
// small set of read verbs that print one formatted line per record, so a model
// can read the battlefield directly:
//
//	saker board
//	saker focus --intent in_x [--layer hot|warm|cold] [--limit N] [--cursor C]
//	saker facts [--ids f_1,f_2] [--q 关键词] [--kind vuln] [--intent in_x] [--limit N] [--cursor C]
//	saker changes [--since N] [--cursor C] [--limit N]
//	saker inbox [--limit N] [--after SEQ] [--box inbox|sent|all]
//	saker readiness --intent in_x
//	saker reviews [--fact f_x]
//	saker wake [--intent in_x]
//
// Connection: $SAKER_BATTLE (the project API base exported by the session shell)
// and $SAKER_AUTH (one ready-made "Authorization: Bearer …" line), optionally
// overridden with --base/--token/--project. The token is never printed.
//
// Every record is one line of `key=value` fields separated by tabs, in a fixed
// order; the last lines are always `truncated=…`, `dropped=…`, `has_more=…`
// and `next_cursor=…`, and a cursor is passed through verbatim. A page is
// capped (16 KiB, wake 32 KiB) and the default page size is small enough to fit
// it; if a body is still clipped, `dropped>0` and `has_more=true` say so and
// the caller retries with a smaller --limit instead of skipping rows. These are
// read-only verbs: writing the ledger stays on the API/curl path.
//
// maybeRunCLI takes over only when the first arg is one of the read verbs;
// anything else (e.g. `saker --addr …`) returns false so the server flags keep
// working.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/retrieval"
)

const (
	// cliMaxBytes bounds one read verb's whole output. It is deliberately larger
	// than the first 4 KiB draft: a page that overflows the cap must either be
	// small enough to fit or be reported as truncated with a retry hint. Silent
	// tail loss is worse than a slightly larger read.
	cliMaxBytes = 16 << 10
	// cliWakeMaxBytes is the one merged digest (`saker wake`); its sections have
	// their own budgets below.
	cliWakeMaxBytes = 32 << 10
	// cliDefaultRows is the server page size the CLI asks for when the caller
	// does not pass --limit. A page must fit cliMaxBytes, because the server
	// cursor is page-granular: the CLI cannot resume inside a page it dropped.
	cliDefaultRows = 10
	// cliFieldRunes is one short label field's cap (titles, ids, statuses).
	cliFieldRunes = 80
	// cliTextRunes is the cap for decision-carrying prose (messages, reasons,
	// fact bodies). A 500-character operator instruction must not become an
	// 80-rune stub.
	cliTextRunes = 400
	// cliFooterReserve is the space kept for the four trailer lines: the opaque
	// cursor has no fixed size, so the body budget is not the cap.
	cliFooterReserve = 420
	// cliHTTPTimeout bounds one request. The CLI talks to the loopback control
	// plane; a hung read must not hold the commander's shell.
	cliHTTPTimeout = 30 * time.Second
)

// cliVerbNames is the closed verb set. A first arg outside it is not ours.
var cliVerbNames = map[string]bool{
	"board":     true,
	"focus":     true,
	"facts":     true,
	"changes":   true,
	"inbox":     true,
	"readiness": true,
	"reviews":   true,
	"wake":      true,
}

// maybeRunCLI runs a read verb when args[0] names one. It returns true after the
// command succeeded (the caller must then return instead of starting the
// server); unknown first args return false untouched. Failures exit with the
// CLI's status code (1 = API/transport, 2 = usage/config).
func maybeRunCLI(args []string) bool {
	if len(args) == 0 || !cliVerbNames[args[0]] {
		return false
	}
	if code := runCLI(args, os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
	return true
}

// runCLI is the testable entry point: same behaviour, caller-owned streams.
func runCLI(args []string, stdout, stderr io.Writer) int {
	return runCLIWithEnv(args, stdout, stderr, os.Getenv)
}

// runCLIWithEnv injects the environment lookup so tests never touch the real
// session variables (and can run in parallel).
func runCLIWithEnv(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 || !cliVerbNames[args[0]] {
		fmt.Fprintln(stderr, "error=invalid_args 未知子命令（可用：board/focus/facts/changes/inbox/readiness/reviews/wake）")
		return 2
	}
	switch args[0] {
	case "board":
		return cliBoard(args[1:], stdout, stderr, getenv)
	case "focus":
		return cliFocus(args[1:], stdout, stderr, getenv)
	case "facts":
		return cliFacts(args[1:], stdout, stderr, getenv)
	case "changes":
		return cliChanges(args[1:], stdout, stderr, getenv)
	case "inbox":
		return cliInbox(args[1:], stdout, stderr, getenv)
	case "readiness":
		return cliReadinessCmd(args[1:], stdout, stderr, getenv)
	case "reviews":
		return cliReviews(args[1:], stdout, stderr, getenv)
	case "wake":
		return cliWake(args[1:], stdout, stderr, getenv)
	}
	return 2
}

// ---- connection ----

type cliConfig struct {
	base    string
	token   string
	project string
}

type cliConn struct {
	base   string
	token  string
	client *http.Client
}

// newCLIFlags creates the flag set every verb shares. Output is discarded:
// usage errors are printed by the CLI itself in its own key=value format.
func newCLIFlags(name string) (*flag.FlagSet, *cliConfig) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := &cliConfig{}
	fs.StringVar(&cfg.base, "base", "", "项目 API 基址（默认 $SAKER_BATTLE；也可给 SAKER_API 根地址）")
	fs.StringVar(&cfg.token, "token", "", "会话令牌（默认 $SAKER_AUTH；不会打印）")
	fs.StringVar(&cfg.project, "project", "", "项目 id（默认 $SAKER_PROJECT）")
	return fs, cfg
}

// cliParse parses flags and rejects stray positional arguments. ok=false means
// stop with the returned code (0 for help).
func cliParse(fs *flag.FlagSet, args []string, stdout, stderr io.Writer) (ok bool, code int) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(stdout, "usage=saker %s [--base URL] [--token TOKEN] [--project ID]\n", fs.Name())
			return false, 0
		}
		fmt.Fprintf(stderr, "error=invalid_args %s\n", cliClean(err.Error()))
		return false, 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "error=invalid_args 多余的参数：%s\n", cliClean(strings.Join(fs.Args(), " ")))
		return false, 2
	}
	return true, 0
}

// resolve finds the project API base and credential. A missing base or token is
// a configuration error (exit 2); neither message ever contains the credential.
func (c *cliConfig) resolve(getenv func(string) string) (cliConn, error) {
	project := strings.TrimSpace(c.project)
	if project == "" {
		project = strings.TrimSpace(getenv("SAKER_PROJECT"))
	}
	base := strings.TrimSpace(c.base)
	if base == "" {
		base = strings.TrimSpace(getenv("SAKER_BATTLE"))
	}
	if base == "" {
		if api := strings.TrimSpace(getenv("SAKER_API")); api != "" {
			if project == "" {
				return cliConn{}, errors.New("缺少项目：设置 SAKER_PROJECT 或用 --project 指定")
			}
			base = strings.TrimRight(api, "/") + "/p/" + project + "/api"
		}
	}
	if base == "" {
		return cliConn{}, errors.New("缺少控制面地址：设置 SAKER_BATTLE（或 SAKER_API + SAKER_PROJECT），或用 --base 指定")
	}
	base = strings.TrimRight(base, "/")
	if !strings.Contains(base, "/p/") {
		if project == "" {
			return cliConn{}, errors.New("基址需要 /p/<项目>/api：用 --project 指定项目")
		}
		base = base + "/p/" + project + "/api"
	}
	token := cliParseToken(c.token)
	if token == "" {
		token = cliParseToken(getenv("SAKER_AUTH"))
	}
	if token == "" {
		return cliConn{}, errors.New("缺少令牌：设置 SAKER_AUTH 或用 --token 指定")
	}
	return cliConn{
		base:   base,
		token:  token,
		client: &http.Client{Timeout: cliHTTPTimeout},
	}, nil
}

// cliParseToken accepts the three shapes the session may hand over: a raw
// token, "Bearer …", or the whole "Authorization: Bearer …" line from .saker/auth.
func cliParseToken(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.Index(strings.ToLower(s), "authorization:"); i >= 0 {
		s = strings.TrimSpace(s[i+len("authorization:"):])
	}
	if len(s) >= 7 && strings.EqualFold(s[:7], "bearer ") {
		s = strings.TrimSpace(s[7:])
	}
	return s
}

// ---- HTTP ----

// cliAPIError is a non-2xx answer; its body is already clipped for printing.
type cliAPIError struct {
	status int
	body   string
}

func (e *cliAPIError) Error() string { return fmt.Sprintf("%d %s", e.status, e.body) }

// get performs one GET and returns the body. The Authorization header carries
// the session credential; it is never logged or returned in an error.
func (c cliConn) get(path string, query url.Values) ([]byte, error) {
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("请求地址无效：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上控制面：%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败：%v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &cliAPIError{status: resp.StatusCode, body: cliClipRunes(cliClean(string(body)), 200)}
	}
	return body, nil
}

func cliDecode(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("解析响应失败：%v", err)
	}
	return nil
}

// ---- errors ----

func cliFail(stderr io.Writer, err error) int {
	var apiErr *cliAPIError
	if errors.As(err, &apiErr) {
		fmt.Fprintf(stderr, "error=%d %s\n", apiErr.status, apiErr.body)
		return 1
	}
	fmt.Fprintf(stderr, "error=request %s\n", cliClean(err.Error()))
	return 1
}

func cliInvalid(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "error=invalid_args %s\n", cliClean(msg))
	return 2
}

func cliConfigFail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "error=config %s\n", cliClean(err.Error()))
	return 2
}

// ---- output ----

// cliWriter is one command's bounded line writer. It stops accepting rows when
// the body budget is spent, remembers that it truncated, and always emits the
// three trailer lines.
type cliWriter struct {
	w         io.Writer
	maxBody   int
	used      int
	truncated bool
	dropped   int
}

func newCLIWriter(w io.Writer, limit int) *cliWriter {
	maxBody := limit - cliFooterReserve
	if maxBody < 256 {
		maxBody = 256
	}
	return &cliWriter{w: w, maxBody: maxBody}
}

func (o *cliWriter) line(s string) {
	if o.used+len(s)+1 > o.maxBody {
		o.truncated = true
		o.dropped++
		return
	}
	fmt.Fprintln(o.w, s)
	o.used += len(s) + 1
}

// cap temporarily lowers this writer's body budget so one wake section cannot
// starve the sections after it. The returned function restores the budget.
func (o *cliWriter) cap(bytes int) func() {
	old := o.maxBody
	if lim := o.used + bytes; lim < o.maxBody {
		o.maxBody = lim
	}
	return func() { o.maxBody = old }
}

func (o *cliWriter) finish(hasMore bool, nextCursor string) {
	// A truncated body has no trustworthy page cursor: the server cursor points
	// after the page, which may include rows this writer dropped. Say the body is
	// incomplete and leave the cursor empty; the caller retries with a smaller
	// --limit instead of skipping the dropped rows.
	if o.truncated {
		hasMore = true
		nextCursor = ""
	}
	fmt.Fprintf(o.w, "truncated=%t\ndropped=%d\nhas_more=%t\nnext_cursor=%s\n",
		o.truncated, o.dropped, hasMore, nextCursor)
}

// cliRecord renders one line: `type=<kind>` then fixed-order key=value fields,
// tab separated. Values are cleaned (no newline/tab/ANSI) and clipped.
func cliRecord(kind string, pairs ...string) string {
	var b strings.Builder
	b.WriteString("type=")
	b.WriteString(kind)
	for i := 0; i+1 < len(pairs); i += 2 {
		b.WriteString("\t")
		b.WriteString(pairs[i])
		b.WriteString("=")
		b.WriteString(cliField(pairs[i+1]))
	}
	return b.String()
}

// cliRecordText is the same record with one long prose field. It keeps the
// fixed key=value shape but does not turn an instruction or a body into an
// 80-rune label.
func cliRecordText(kind, key, text string, pairs ...string) string {
	var b strings.Builder
	b.WriteString("type=")
	b.WriteString(kind)
	for i := 0; i+1 < len(pairs); i += 2 {
		b.WriteString("\t")
		b.WriteString(pairs[i])
		b.WriteString("=")
		b.WriteString(cliField(pairs[i+1]))
	}
	b.WriteString("\t")
	b.WriteString(key)
	b.WriteString("=")
	b.WriteString(cliLongField(text))
	return b.String()
}

// cliField cleans one free-text value and clips it to cliFieldRunes runes
// (including the ellipsis).
func cliField(s string) string {
	return cliClipRunes(cliClean(s), cliFieldRunes)
}

// cliLongField is for prose the model must act on (message text, review notes,
// a fact body): longer than a label, still one line.
func cliLongField(s string) string {
	return cliClipRunes(cliClean(s), cliTextRunes)
}

// cliClean makes a value safe for a one-line key=value record: no line breaks,
// no tabs, no other control bytes, no ANSI escape sequences.
func cliClean(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b {
			// Skip a CSI/OSC escape sequence; the CLI never emits ANSI itself,
			// but a ledger title may carry one copied from tool output.
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				if i < len(s) {
					i++
				}
			}
			continue
		}
		if c == '\n' || c == '\r' || c == '\t' {
			b.WriteByte(' ')
			i++
			continue
		}
		if c < 0x20 || c == 0x7f {
			i++
			continue
		}
		b.WriteByte(c)
		i++
	}
	return strings.TrimSpace(b.String())
}

// cliClipRunes clips s to n runes inclusive of the ellipsis.
func cliClipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func cliInt(n int) string     { return strconv.Itoa(n) }
func cliUint(n uint64) string { return strconv.FormatUint(n, 10) }
func cliBool(b bool) string   { return strconv.FormatBool(b) }

func cliTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// cliPageQuery builds the shared read-page query values.
func cliPageQuery(values map[string]string, limit int) url.Values {
	q := url.Values{}
	for k, v := range values {
		if strings.TrimSpace(v) != "" {
			q.Set(k, v)
		}
	}
	if limit > 0 {
		q.Set("limit", cliInt(limit))
	}
	return q
}

// ---- board ----

type cliBoardResponse struct {
	board.View
	ProjectPaused    bool   `json:"project_paused"`
	ProjectPauseKind string `json:"project_pause_kind,omitempty"`
}

func cliBoard(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs, cfg := newCLIFlags("board")
	if ok, code := cliParse(fs, args, stdout, stderr); !ok {
		return code
	}
	conn, err := cfg.resolve(getenv)
	if err != nil {
		return cliConfigFail(stderr, err)
	}
	body, err := conn.get("/board", nil)
	if err != nil {
		return cliFail(stderr, err)
	}
	var resp cliBoardResponse
	if err := cliDecode(body, &resp); err != nil {
		return cliFail(stderr, err)
	}
	out := newCLIWriter(stdout, cliMaxBytes)
	renderBoard(out, &resp)
	out.finish(false, "")
	return 0
}

func renderBoard(out *cliWriter, resp *cliBoardResponse) {
	v := resp.View
	out.line(cliRecord("project",
		"paused", cliBool(resp.ProjectPaused),
		"pause_kind", resp.ProjectPauseKind))
	out.line(cliRecord("counts", "scope", "intents",
		"total", cliInt(v.Intents.Total),
		"pending", cliInt(v.Intents.Pending),
		"held", cliInt(v.Intents.Held),
		"claimed", cliInt(v.Intents.Claimed),
		"running", cliInt(v.Intents.Running),
		"waiting", cliInt(v.Intents.Waiting),
		"reviewing", cliInt(v.Intents.Reviewing),
		"done", cliInt(v.Intents.Done),
		"failed", cliInt(v.Intents.Failed),
		"review_failed", cliInt(v.Intents.ReviewFailed),
		"parked", cliInt(v.Intents.Parked)))
	out.line(cliRecord("counts", "scope", "facts",
		"total", cliInt(v.Facts.Total),
		"verified", cliInt(v.Facts.Verified),
		"unverified", cliInt(v.Facts.Unverified),
		"rejected", cliInt(v.Facts.Rejected),
		"with_evidence", cliInt(v.Facts.WithEvidence)))
	out.line(cliRecord("counts", "scope", "runs",
		"total", cliInt(v.Runs.Total),
		"running", cliInt(v.Runs.Running),
		"done", cliInt(v.Runs.Done),
		"failed", cliInt(v.Runs.Failed),
		"canceled", cliInt(v.Runs.Canceled)))
	out.line(cliRecord("counts", "scope", "goals",
		"total", cliInt(v.Goals.Total),
		"met", cliInt(v.Goals.Met),
		"open", cliInt(len(v.Goals.Open)),
		"needs_review", cliInt(len(v.Goals.NeedsReview))))
	out.line(cliRecord("counts", "scope", "evidence", "total", cliInt(v.Evidence)))
	out.line(cliRecord("counts", "scope", "playbooks",
		"total", cliInt(v.Playbooks.Total),
		"confirmed", cliInt(v.Playbooks.Confirmed),
		"candidates", cliInt(v.Playbooks.Candidates)))
	out.line(cliRecord("counts", "scope", "redispatch",
		"directions", cliInt(v.Redispatch.Directions),
		"extra_attempts", cliInt(v.Redispatch.Total)))
	// The goal statement is the anchor a cold-loaded commander has to see; the
	// count alone cannot say what "done" means.
	for _, g := range v.Goals.Open {
		out.line(cliRecordText("goal", "statement", g.Statement,
			"state", "open",
			"id", g.ID,
			"start", g.Start))
	}
	for _, g := range v.Goals.NeedsReview {
		out.line(cliRecordText("goal", "statement", g.Statement,
			"state", "needs_review",
			"id", g.ID,
			"changed_facts", strings.Join(g.ChangedFacts, ",")))
	}
	if v.Intents.Parked > 0 {
		out.line(cliRecord("notice", "text",
			fmt.Sprintf("parked=%d：board 只给计数；意图 id 用 /api/intents?status=parked", v.Intents.Parked)))
	}
	for _, it := range v.Open {
		out.line(cliRecord("intent", "status", "open",
			"id", it.ID,
			"title", it.Title,
			"attempts", cliInt(it.Attempts)))
	}
	for _, it := range v.Held {
		out.line(cliRecord("intent", "status", "held",
			"id", it.ID,
			"title", it.Title,
			"attempts", cliInt(it.Attempts)))
	}
	// Waiting/reviewing/review_failed are direction lifecycle states, not
	// "nothing to do": a cold-loaded commander that only sees their counts would
	// dispatch around a blocked child or an interrupted review.
	for _, group := range []struct {
		status string
		items  []board.Item
	}{
		{"waiting", v.Waiting},
		{"reviewing", v.Reviewing},
		{"review_failed", v.ReviewFailed},
	} {
		for _, it := range group.items {
			out.line(cliRecord("intent", "status", group.status,
				"id", it.ID,
				"title", it.Title,
				"attempts", cliInt(it.Attempts),
				"profile", it.Profile,
				"tier", it.Tier))
		}
	}
	// The frontier is the dispatch view: an open direction whose premise was
	// rejected is dead work, not pending work.
	for _, f := range v.Frontier {
		out.line(cliRecord("frontier",
			"id", f.ID,
			"title", f.Title,
			"status", f.Status,
			"ready", cliBool(f.Ready),
			"prerequisites", cliInt(f.Prerequisites),
			"no_premise", cliBool(f.NoPremise),
			"blocked", strings.Join(f.Blocked, ","),
			"premise_changed", strings.Join(f.PremiseChanged, ",")))
	}
	// Done titles only: the ledger has no detail row for them in the board.
	for _, title := range v.Done {
		out.line(cliRecord("done", "title", title))
	}
	for _, it := range v.Running {
		out.line(cliRecord("intent", "status", "running",
			"id", it.ID,
			"title", it.Title,
			"attempts", cliInt(it.Attempts)))
		if it.Pulse != nil {
			out.line(cliRecord("pulse",
				"intent", it.ID,
				"idle_seconds", cliInt(it.Pulse.IdleSeconds),
				"events", cliInt(it.Pulse.Events),
				"declared_facts", cliInt(it.Pulse.DeclaredFacts),
				"tool_failures", cliInt(it.Pulse.ToolFailures),
				"last_kind", it.Pulse.LastKind,
				"last_tool", it.Pulse.LastTool,
				"last_text", it.Pulse.LastText))
		}
	}
	for _, it := range v.RecentFacts {
		out.line(cliRecord("fact",
			"id", it.ID,
			"kind", it.Kind,
			"status", it.Status,
			"origin", it.Origin,
			"evidence", cliInt(it.Evidence),
			"intent", it.IntentID,
			"title", it.Title))
	}
}

// ---- focus ----

func cliFocus(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs, cfg := newCLIFlags("focus")
	intent := fs.String("intent", "", "意图 id（缺省取 $SAKER_SELF）")
	layer := fs.String("layer", "", "hot|warm|cold（缺省全部）")
	cursor := fs.String("cursor", "", "上一页返回的 next_cursor（原样透传）")
	limit := fs.Int("limit", 0, "每页条数（0 = CLI 默认页大小）")
	if ok, code := cliParse(fs, args, stdout, stderr); !ok {
		return code
	}
	if *limit < 0 {
		return cliInvalid(stderr, "--limit 必须是非负整数")
	}
	intentID := strings.TrimSpace(*intent)
	if intentID == "" {
		intentID = strings.TrimSpace(getenv("SAKER_SELF"))
	}
	if intentID == "" {
		return cliInvalid(stderr, "--intent 必填（或设置 SAKER_SELF）")
	}
	layerName := strings.TrimSpace(*layer)
	switch layerName {
	case "", "hot", "warm", "cold":
	default:
		return cliInvalid(stderr, "--layer 只能是 hot/warm/cold")
	}
	conn, err := cfg.resolve(getenv)
	if err != nil {
		return cliConfigFail(stderr, err)
	}
	if *limit == 0 {
		*limit = cliDefaultRows
	}
	q := cliPageQuery(map[string]string{"intent": intentID, "layer": layerName, "cursor": *cursor}, *limit)
	body, err := conn.get("/focus", q)
	if err != nil {
		return cliFail(stderr, err)
	}
	var page retrieval.FocusPage
	if err := cliDecode(body, &page); err != nil {
		return cliFail(stderr, err)
	}
	out := newCLIWriter(stdout, cliMaxBytes)
	renderFocus(out, &page)
	out.finish(page.HasMore, page.NextCursor)
	return 0
}

func renderFocus(out *cliWriter, page *retrieval.FocusPage) {
	out.line(cliRecord("counts", "scope", "focus",
		"intent", page.Intent,
		"layer", page.Layer,
		"hot", cliInt(page.Hot),
		"warm", cliInt(page.Warm),
		"cold", cliInt(page.Cold),
		"total", cliInt(page.Total)))
	for _, f := range page.Facts {
		out.line(cliRecordText("fact", "summary", f.Summary,
			"scope", "focus",
			"id", f.ID,
			"kind", f.Kind,
			"status", f.Status,
			"temperature", f.Temperature,
			"reasons", strings.Join(f.Reasons, ","),
			"claim_version", cliUint(f.ClaimVersion),
			"title", f.Title))
	}
	// Print the page cursor only after the rows. A cursor printed before a
	// clipped body would let the caller jump past the rows this writer dropped;
	// the trailer then deliberately reports truncated/has_more with no cursor
	// and the caller retries with a smaller --limit.
	out.line(cliRecord("focus_page",
		"intent", page.Intent,
		"has_more", cliBool(page.HasMore),
		"next_cursor", page.NextCursor,
		"total", cliInt(page.Total)))
}

// ---- facts ----

func cliFacts(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs, cfg := newCLIFlags("facts")
	ids := fs.String("ids", "", "按 id 读取：f_1,f_2")
	query := fs.String("q", "", "关键词")
	kind := fs.String("kind", "", "事实类型")
	intent := fs.String("intent", "", "来源意图 id")
	cursor := fs.String("cursor", "", "上一页返回的 next_cursor（原样透传）")
	limit := fs.Int("limit", 0, "每页条数（0 = CLI 默认页大小）")
	if ok, code := cliParse(fs, args, stdout, stderr); !ok {
		return code
	}
	if *limit < 0 {
		return cliInvalid(stderr, "--limit 必须是非负整数")
	}
	if *limit == 0 {
		*limit = cliDefaultRows
	}
	conn, err := cfg.resolve(getenv)
	if err != nil {
		return cliConfigFail(stderr, err)
	}
	out := newCLIWriter(stdout, cliMaxBytes)

	var factIDs []string
	for _, id := range strings.Split(*ids, ",") {
		if id = strings.TrimSpace(id); id != "" {
			factIDs = append(factIDs, id)
		}
	}
	if len(factIDs) > 0 {
		// The list endpoint cannot filter by id; one depth read per id is the
		// honest shape and keeps the total request count visible to the model.
		facts := make([]model.Fact, 0, len(factIDs))
		for _, id := range factIDs {
			body, err := conn.get("/facts/"+url.PathEscape(id), nil)
			if err != nil {
				return cliFail(stderr, err)
			}
			var one struct {
				Fact model.Fact `json:"fact"`
			}
			if err := cliDecode(body, &one); err != nil {
				return cliFail(stderr, err)
			}
			facts = append(facts, one.Fact)
		}
		renderFactDetails(out, facts)
		out.finish(false, "")
		return 0
	}

	q := cliPageQuery(map[string]string{
		"q": *query, "kind": *kind, "intent": *intent, "cursor": *cursor,
	}, *limit)
	body, err := conn.get("/facts", q)
	if err != nil {
		return cliFail(stderr, err)
	}
	var page retrieval.FactPage
	if err := cliDecode(body, &page); err != nil {
		return cliFail(stderr, err)
	}
	renderFacts(out, page.Facts)
	out.finish(page.HasMore, page.NextCursor)
	return 0
}

func renderFacts(out *cliWriter, facts []model.Fact) {
	for _, f := range facts {
		out.line(cliRecord("fact",
			"id", f.ID,
			"kind", f.Kind,
			"status", f.Status,
			"intent", f.IntentID,
			"origin", f.Origin,
			"confidence", f.Confidence,
			"evidence", cliInt(len(f.EvidenceIDs)),
			"title", f.Title,
			"summary", f.Summary))
	}
}

// renderFactDetails is the depth projection for --ids. The list row keeps the
// same shape as `saker facts`; the extra rows carry the body and evidence ids the
// list deliberately omits, so "facts --ids f_x" is actually a depth read.
func renderFactDetails(out *cliWriter, facts []model.Fact) {
	for _, f := range facts {
		out.line(cliRecord("fact",
			"id", f.ID,
			"kind", f.Kind,
			"status", f.Status,
			"intent", f.IntentID,
			"origin", f.Origin,
			"confidence", f.Confidence,
			"evidence", cliInt(len(f.EvidenceIDs)),
			"claim_version", cliUint(f.ClaimVersion),
			"title", f.Title))
		if body := strings.TrimSpace(f.Doc); body != "" {
			out.line(cliRecordText("fact_doc", "doc", body, "fact", f.ID, "bytes", cliInt(len(body))))
		}
		if len(f.EvidenceIDs) > 0 {
			out.line(cliRecord("fact_evidence",
				"fact", f.ID,
				"ids", strings.Join(f.EvidenceIDs, ","),
				"count", cliInt(len(f.EvidenceIDs))))
		}
	}
}

// ---- changes ----

func cliChanges(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs, cfg := newCLIFlags("changes")
	since := fs.String("since", "", "只看该 watermark 之后的变更（数字）")
	cursor := fs.String("cursor", "", "上一页返回的 next_cursor（原样透传）")
	limit := fs.Int("limit", 0, "每页条数（0 = CLI 默认页大小）")
	if ok, code := cliParse(fs, args, stdout, stderr); !ok {
		return code
	}
	if *limit < 0 {
		return cliInvalid(stderr, "--limit 必须是非负整数")
	}
	if *limit == 0 {
		*limit = cliDefaultRows
	}
	values := map[string]string{"cursor": *cursor}
	if s := strings.TrimSpace(*since); s != "" {
		if _, err := strconv.ParseUint(s, 10, 64); err != nil {
			return cliInvalid(stderr, "--since 必须是非负整数")
		}
		values["since"] = s
	}
	conn, err := cfg.resolve(getenv)
	if err != nil {
		return cliConfigFail(stderr, err)
	}
	q := cliPageQuery(values, *limit)
	body, err := conn.get("/changes", q)
	if err != nil {
		return cliFail(stderr, err)
	}
	var page retrieval.ChangePage
	if err := cliDecode(body, &page); err != nil {
		return cliFail(stderr, err)
	}
	out := newCLIWriter(stdout, cliMaxBytes)
	out.line(cliRecord("window",
		"epoch", page.HighWatermark.Epoch,
		"seq", cliUint(page.HighWatermark.Seq),
		"pending", cliInt(page.Pending)))
	for _, c := range page.Changes {
		out.line(cliRecord("change",
			"seq", cliUint(c.Seq),
			"kind", c.Kind,
			"operation", c.Operation,
			"id", c.ID,
			"version", cliUint(c.Version),
			"at", cliTime(c.At)))
	}
	out.finish(page.HasMore, page.NextCursor)
	return 0
}

// ---- inbox ----

func cliInbox(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs, cfg := newCLIFlags("inbox")
	limit := fs.Int("limit", cliDefaultRows, "最多读取的消息条数")
	after := fs.Uint64("after", 0, "只读取该 seq 之后的消息（agent 邮箱游标）")
	box := fs.String("box", "", "agent 邮箱：inbox|sent|all（缺省服务端语义）")
	if ok, code := cliParse(fs, args, stdout, stderr); !ok {
		return code
	}
	if *limit <= 0 {
		return cliInvalid(stderr, "--limit 必须是正整数")
	}
	conn, agent, err := cfg.resolveMailbox(getenv)
	if err != nil {
		return cliConfigFail(stderr, err)
	}
	q := url.Values{}
	q.Set("limit", cliInt(*limit))
	if *after > 0 {
		q.Set("after", cliUint(*after))
	}
	if b := strings.TrimSpace(*box); b != "" {
		q.Set("box", b)
	}
	body, err := conn.get("/messages", q)
	if err != nil {
		return cliFail(stderr, err)
	}
	var resp struct {
		Messages []model.Message `json:"messages"`
		Next     uint64          `json:"next"`
	}
	if err := cliDecode(body, &resp); err != nil {
		return cliFail(stderr, err)
	}
	out := newCLIWriter(stdout, cliMaxBytes)
	nextCursor := ""
	if agent && resp.Next > 0 {
		nextCursor = cliUint(resp.Next)
	}
	scope := "project"
	if agent {
		scope = "agent"
	}
	renderInbox(out, resp.Messages)
	// The next sequence is printed after the rows for the same reason as the
	// focus cursor: a clipped body must not print a cursor that skips its tail.
	out.line(cliRecord("inbox_page",
		"scope", scope,
		"count", cliInt(len(resp.Messages)),
		"next", nextCursor))
	out.finish(false, nextCursor)
	return 0
}

// resolveMailbox chooses the participant mailbox when the shell exported one
// (SAKER_AGENT). Every session, including the commander, has its own unread queue;
// the project's /p/messages endpoint is the operator's whole-project view and
// would neither show what is unread nor advance the after/box cursor. A shell
// without SAKER_AGENT (old host, tests) falls back to the project view.
func (c *cliConfig) resolveMailbox(getenv func(string) string) (cliConn, bool, error) {
	conn, err := c.resolve(getenv)
	if err != nil {
		return cliConn{}, false, err
	}
	agent := strings.TrimRight(strings.TrimSpace(getenv("SAKER_AGENT")), "/")
	if agent == "" {
		return conn, false, nil
	}
	return cliConn{base: agent, token: conn.token, client: conn.client}, true, nil
}

func renderInbox(out *cliWriter, msgs []model.Message) {
	for _, m := range msgs {
		out.line(cliRecordText("message", "text", m.Text,
			"id", m.ID,
			"seq", cliUint(m.Seq),
			"from", m.FromParticipantID,
			"to", m.ToParticipantID,
			"kind", m.Kind,
			"status", m.Status,
			"in_reply_to", m.InReplyTo,
			"at", cliTime(m.CreatedAt)))
	}
}

// ---- readiness ----

type cliReadiness struct {
	IntegrityChecked  bool   `json:"integrity_checked"`
	IntentID          string `json:"intent_id"`
	Policy            string `json:"policy"`
	Ready             bool   `json:"ready"`
	ReviewHint        string `json:"review_hint,omitempty"`
	SelfReferenceHint string `json:"self_reference_hint,omitempty"`
	Premises          []struct {
		ID           string `json:"id"`
		Title        string `json:"title,omitempty"`
		ClaimVersion uint64 `json:"claim_version,omitempty"`
		Status       string `json:"status"`
		Usable       bool   `json:"usable"`
		Reason       string `json:"reason,omitempty"`
		Integrity    string `json:"integrity"`
		Review       *struct {
			State string `json:"state"`
		} `json:"review,omitempty"`
	} `json:"premises"`
}

func cliReadinessCmd(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs, cfg := newCLIFlags("readiness")
	intent := fs.String("intent", "", "意图 id（缺省取 $SAKER_SELF）")
	if ok, code := cliParse(fs, args, stdout, stderr); !ok {
		return code
	}
	intentID := strings.TrimSpace(*intent)
	if intentID == "" {
		intentID = strings.TrimSpace(getenv("SAKER_SELF"))
	}
	if intentID == "" {
		return cliInvalid(stderr, "--intent 必填（或设置 SAKER_SELF）")
	}
	conn, err := cfg.resolve(getenv)
	if err != nil {
		return cliConfigFail(stderr, err)
	}
	body, err := conn.get("/intents/"+url.PathEscape(intentID)+"/readiness", nil)
	if err != nil {
		return cliFail(stderr, err)
	}
	var readiness cliReadiness
	if err := cliDecode(body, &readiness); err != nil {
		return cliFail(stderr, err)
	}
	out := newCLIWriter(stdout, cliMaxBytes)
	renderReadiness(out, &readiness)
	out.finish(false, "")
	return 0
}

func renderReadiness(out *cliWriter, r *cliReadiness) {
	out.line(cliRecord("readiness",
		"intent", r.IntentID,
		"policy", r.Policy,
		"ready", cliBool(r.Ready),
		"integrity_checked", cliBool(r.IntegrityChecked)))
	for _, p := range r.Premises {
		reviewState := ""
		if p.Review != nil {
			reviewState = p.Review.State
		}
		out.line(cliRecordText("premise", "reason", p.Reason,
			"id", p.ID,
			"status", p.Status,
			"usable", cliBool(p.Usable),
			"integrity", p.Integrity,
			"claim_version", cliUint(p.ClaimVersion),
			"review", reviewState,
			"title", p.Title))
	}
	if r.ReviewHint != "" {
		out.line(cliRecordText("hint", "text", r.ReviewHint))
	}
	if r.SelfReferenceHint != "" {
		out.line(cliRecordText("hint", "text", r.SelfReferenceHint, "kind", "self_reference"))
	}
}

// ---- reviews ----

func cliReviews(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs, cfg := newCLIFlags("reviews")
	fact := fs.String("fact", "", "只看某条事实的复核")
	if ok, code := cliParse(fs, args, stdout, stderr); !ok {
		return code
	}
	conn, err := cfg.resolve(getenv)
	if err != nil {
		return cliConfigFail(stderr, err)
	}
	factID := strings.TrimSpace(*fact)
	q := cliPageQuery(map[string]string{"fact": factID}, 0)
	body, err := conn.get("/reviews", q)
	var reviews struct {
		Reviews []model.Review `json:"reviews"`
	}
	if err != nil {
		// /api/reviews is the operator's review surface; a harness session's
		// per-round credential gets 404 there by design (the gate hides it). The
		// same verdict rows ride the fact depth read, which is on the session's
		// surface, so a per-fact query still answers instead of dead-ending.
		var apiErr *cliAPIError
		if factID == "" || !errors.As(err, &apiErr) || apiErr.status != http.StatusNotFound {
			return cliFail(stderr, err)
		}
		body, err = conn.get("/facts/"+url.PathEscape(factID), nil)
		if err != nil {
			return cliFail(stderr, err)
		}
	}
	if err := cliDecode(body, &reviews); err != nil {
		return cliFail(stderr, err)
	}
	out := newCLIWriter(stdout, cliMaxBytes)
	renderReviews(out, reviews.Reviews)
	out.finish(false, "")
	return 0
}

func renderReviews(out *cliWriter, reviews []model.Review) {
	for _, r := range reviews {
		out.line(cliRecordText("review", "notes", r.Notes,
			"id", r.ID,
			"fact", r.FactID,
			"verdict", r.Verdict,
			"claim_version", cliUint(r.ClaimVersion),
			"verifier_run", r.VerifierRun,
			"at", cliTime(r.CreatedAt)))
	}
}

// ---- wake ----

// cliWake is the one merged digest: it reads board + inbox and, when the
// caller's own direction is known, focus + readiness in a single invocation —
// one network round of reads, not four process calls. The sections are
// delimited with `## name`; each has its own budget inside cliWakeMaxBytes.
// A truncated section still prints its page cursor (focus/inbox) before the
// rows, so "the digest was clipped" never becomes "there is no more".
func cliWake(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs, cfg := newCLIFlags("wake")
	intent := fs.String("intent", "", "意图 id（缺省取 $SAKER_SELF）")
	if ok, code := cliParse(fs, args, stdout, stderr); !ok {
		return code
	}
	intentID := strings.TrimSpace(*intent)
	if intentID == "" {
		intentID = strings.TrimSpace(getenv("SAKER_SELF"))
	}
	conn, err := cfg.resolve(getenv)
	if err != nil {
		return cliConfigFail(stderr, err)
	}

	// Read everything first: a failed read must print one error and no partial
	// digest, so the model never mistakes a truncated round for a whole board.
	boardBody, err := conn.get("/board", nil)
	if err != nil {
		return cliFail(stderr, err)
	}
	inboxConn, agentInbox, err := cfg.resolveMailbox(getenv)
	if err != nil {
		return cliConfigFail(stderr, err)
	}
	inboxQ := url.Values{}
	inboxQ.Set("limit", cliInt(cliDefaultRows))
	inboxBody, err := inboxConn.get("/messages", inboxQ)
	if err != nil {
		return cliFail(stderr, err)
	}
	var focusPage *retrieval.FocusPage
	var readiness *cliReadiness
	if intentID != "" {
		focusQ := cliPageQuery(map[string]string{"intent": intentID}, cliDefaultRows)
		focusBody, err := conn.get("/focus", focusQ)
		if err != nil {
			return cliFail(stderr, err)
		}
		readinessBody, err := conn.get("/intents/"+url.PathEscape(intentID)+"/readiness", nil)
		if err != nil {
			return cliFail(stderr, err)
		}
		focusPage = &retrieval.FocusPage{}
		if err := cliDecode(focusBody, focusPage); err != nil {
			return cliFail(stderr, err)
		}
		readiness = &cliReadiness{}
		if err := cliDecode(readinessBody, readiness); err != nil {
			return cliFail(stderr, err)
		}
	}
	var boardResp cliBoardResponse
	if err := cliDecode(boardBody, &boardResp); err != nil {
		return cliFail(stderr, err)
	}
	var inbox struct {
		Messages []model.Message `json:"messages"`
		Next     uint64          `json:"next"`
	}
	if err := cliDecode(inboxBody, &inbox); err != nil {
		return cliFail(stderr, err)
	}

	out := newCLIWriter(stdout, cliWakeMaxBytes)
	writeSection := func(title string, budget int, render func()) {
		out.line("## " + title)
		restore := out.cap(budget)
		render()
		restore()
	}
	writeSection("board", 8<<10, func() { renderBoard(out, &boardResp) })
	switch {
	case focusPage != nil:
		writeSection("focus", 8<<10, func() { renderFocus(out, focusPage) })
	default:
		writeSection("focus", 1<<10, func() {
			out.line(cliRecord("notice", "text", "未提供 --intent 或 SAKER_SELF：跳过焦点"))
		})
	}
	writeSection("inbox", 6<<10, func() {
		next := ""
		if agentInbox && inbox.Next > 0 {
			next = cliUint(inbox.Next)
		}
		scope := "project"
		if agentInbox {
			scope = "agent"
		}
		renderInbox(out, inbox.Messages)
		out.line(cliRecord("inbox_page",
			"scope", scope,
			"count", cliInt(len(inbox.Messages)),
			"next", next))
	})
	switch {
	case readiness != nil:
		writeSection("readiness", 6<<10, func() { renderReadiness(out, readiness) })
	default:
		writeSection("readiness", 1<<10, func() {
			out.line(cliRecord("notice", "text", "未提供 --intent 或 SAKER_SELF：跳过准入"))
		})
	}
	hasMore, cursor := false, ""
	if focusPage != nil {
		hasMore, cursor = focusPage.HasMore, focusPage.NextCursor
	}
	out.finish(hasMore, cursor)
	return 0
}
