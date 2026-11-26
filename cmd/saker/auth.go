package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kksty/saker/internal/aop"
	"github.com/kksty/saker/internal/api"
	"github.com/kksty/saker/internal/collab"
)

// Console authentication: one operator token, one signed cookie, one small login
// page. It exists because a control plane that can dispatch offensive work must
// not be reachable by anyone who finds the port; it is deliberately the smallest
// thing that is actually a gate (no user table, no password reset, no sessions
// store — the cookie is self-contained and expires).
//
// Harness sessions (commander and executors) authenticate with their own
// one-run credential, not a shared derived key: the same control plane, but only
// the battlefield surface exists for them — everything else (settings, projects,
// reports, review triggers, worker controls, imports, bundles, …) is absent
// (D109). The operator token never enters the host environment those sessions
// run in, and neither does any other bearer: every session on a host shares that
// environment, so a credential placed there belongs to all of them.
type consoleAuth struct {
	// key is the operator token's hash, compared in constant time.
	key []byte
	// sessions resolves a harness session's per-run credential (the commander's
	// per-session identity; executors reach this gate when they read the shared
	// battlefield). Nil disables the session credential, as in the login-form
	// tests.
	sessions sessionResolver
	ttl      time.Duration
	// limiter counts wrong guesses at the login form. It is deliberately a
	// separate bucket from bearer: a stale token retried by a harness session
	// must not lock the human out of /login (both come from 127.0.0.1).
	limiter *loginLimiter
	// bearer counts failed bearer attempts. It bounds a retry loop without
	// spending the form's budget.
	bearer *loginLimiter
	// read resolves a review run's read-only credential. Nil when the resolver
	// has no read-only side (login-form tests), which only means no such
	// credential is accepted.
	read readOnlyResolver
}

// sessionResolver is the collab registry's read side. The gate only needs to
// map a bearer to the identity it was issued for; the project binding is
// enforced from Identity.ProjectID.
type sessionResolver interface {
	Resolve(string) (collab.Identity, bool)
}

// readOnlyResolver is the registry's read-only side. It is a separate interface
// so the two credential kinds cannot be confused: a bearer resolves to either an
// Identity (a session) or a ReadOnlyCredential (a support run), never to both.
type readOnlyResolver interface {
	ResolveReadOnly(string) (collab.ReadOnlyCredential, bool)
}

// consoleRole is what an authenticated credential may reach.
type consoleRole int

const (
	// roleOperator is the human console: everything, including reports/reviews.
	roleOperator consoleRole = iota
	// roleAgent is a harness session (commander or executor): the battlefield
	// surface only, so it focuses on the engagement instead of the operator's
	// controls.
	roleAgent
	// roleVerifier is a review run's read-only credential: the fact it must
	// re-prove, the evidence that fact cites, and the run events that say what
	// happened. It is deliberately not roleAgent — a reviewer that could speak as
	// a participant or browse the whole battlefield would become a second
	// direction instead of an independent second pair of eyes, and independence
	// here is "recompute from the originals", not "see everything".
	roleVerifier
)

const (
	consoleCookie = "saker_session"
	consoleTTL    = 12 * time.Hour

	// loginMaxFailures failures per IP inside loginWindow park that IP for the
	// rest of the window. A strong token cannot be guessed; this exists so the
	// try loop is not free: it costs CPU, fills the log, and turns a typo into
	// an oracle.
	loginMaxFailures = 8
	loginWindow      = 10 * time.Minute
)

// loginLimiter is the in-memory failure window for one console. A restart clears
// it, which is fine: the token remains the gate, this only bounds the attempts.
type loginLimiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	fails  map[string][]time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	if max <= 0 {
		max = loginMaxFailures
	}
	if window <= 0 {
		window = loginWindow
	}
	return &loginLimiter{window: window, max: max, fails: make(map[string][]time.Time)}
}

// retryAfter reports how long the IP must wait. Zero means "not blocked". It
// also prunes the failures that have aged out, so no cleanup goroutine is needed.
func (l *loginLimiter) retryAfter(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	kept := l.fails[ip][:0]
	for _, at := range l.fails[ip] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, ip)
		return 0
	}
	l.fails[ip] = kept
	if len(kept) < l.max {
		return 0
	}
	return kept[0].Add(l.window).Sub(now)
}

func (l *loginLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[ip] = append(l.fails[ip], now)
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}

// clientIP is the peer address. X-Forwarded-For is deliberately not trusted:
// behind a reverse proxy it would let any client pick its own bucket, and an
// operator who does run a proxy can still rate-limit there.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// throttled writes the 429 shared by the login form and the API gate. The
// Retry-After header keeps scripts honest without a second round trip.
func (a *consoleAuth) throttled(w http.ResponseWriter, retry time.Duration, wantsJSON bool) {
	seconds := int(retry.Seconds() + 0.5)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	if wantsJSON {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"尝试过多，请稍后再试"}`))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(renderLogin("", `<div class="err">尝试过多，请稍后再试</div>`)))
}

// newConsoleAuth returns nil when no token is configured: local development
// stays open, and the caller logs that fact loudly. sessions is the collab
// registry; nil means no harness-session credential is accepted (login tests).
func newConsoleAuth(token string, sessions sessionResolver) *consoleAuth {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(token))
	a := &consoleAuth{
		key:      sum[:],
		sessions: sessions,
		ttl:      consoleTTL,
		limiter:  newLoginLimiter(loginMaxFailures, loginWindow),
		bearer:   newLoginLimiter(loginMaxFailures, loginWindow),
	}
	if rd, ok := sessions.(readOnlyResolver); ok {
		a.read = rd
	}
	return a
}

func (a *consoleAuth) issue(w http.ResponseWriter) {
	exp := time.Now().Add(a.ttl).Unix()
	value := fmt.Sprintf("%d.%s", exp, a.sign(exp))
	http.SetCookie(w, &http.Cookie{
		Name:     consoleCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.ttl.Seconds()),
	})
}

func (a *consoleAuth) sign(exp int64) string {
	mac := hmac.New(sha256.New, a.key)
	fmt.Fprintf(mac, "saker-session:%d", exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// sessionScope is a harness credential's reach on the project mux: the project
// it is bound to, whether it is the commander's, and the resolved identity. Both
// roles may read the battlefield there (that visibility is the point of the
// fact-intent graph); only the commander writes on this surface — an executor
// acts through /agent/ with the same identity, where every handler can scope by
// intent.
//
// The identity is not decoration: the project mux registers the same handlers as
// the participant mux, and several of them narrow their read when an identity is
// present (another direction's run output/context, a browser binding without a
// lease, the live session of someone else's browser). Attaching it here is what
// keeps that narrowing true on both bases instead of only on /agent/.
type sessionScope struct {
	project   string
	commander bool
	identity  collab.Identity
}

// authorize checks the credential without touching any server state. A bearer
// token is accepted too: scripts and curl should not have to log in through the
// HTML form, and the harness ecosystem already speaks Authorization: Bearer.
// Two bearers are understood: the operator token (full API) and a harness
// session's own per-run credential (battlefield surface only, D109). The second
// one resolves to an identity, and its project/role are returned so the gate can
// bind the request instead of trusting the URL or a header.
func (a *consoleAuth) authorize(r *http.Request) (consoleRole, sessionScope, bool) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		sum := sha256.Sum256([]byte(got))
		if subtle.ConstantTimeCompare(sum[:], a.key) == 1 {
			return roleOperator, sessionScope{}, true
		}
		if a.sessions != nil {
			if ident, ok := a.sessions.Resolve(got); ok {
				return roleAgent, sessionScope{
					project:   ident.ProjectID,
					commander: ident.ParticipantID == collab.CommanderID,
					identity:  ident,
				}, true
			}
		}
		if a.read != nil {
			if cred, ok := a.read.ResolveReadOnly(got); ok {
				return roleVerifier, sessionScope{project: cred.ProjectID}, true
			}
		}
	}
	c, err := r.Cookie(consoleCookie)
	if err != nil {
		return roleOperator, sessionScope{}, false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return roleOperator, sessionScope{}, false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return roleOperator, sessionScope{}, false
	}
	want := a.sign(exp)
	// Only the operator's browser gets a cookie; harness sessions talk to the
	// API with their bearer token and cannot mint one.
	if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(want)) == 1 {
		return roleOperator, sessionScope{}, true
	}
	return roleOperator, sessionScope{}, false
}

// pathProject is the project a /p/<id>/... path names, or "" for the
// instance-level routes. The gate uses it to bind a session credential to its
// own project: a commander's per-session token is one session's credential, not an
// instance-wide key, and a token for project A must not address project B.
func pathProject(path string) string {
	rest, ok := strings.CutPrefix(path, "/p/")
	if !ok {
		return ""
	}
	id, _, ok := strings.Cut(rest, "/")
	if !ok || id == "" {
		return ""
	}
	return id
}

// isCommanderInboxPath covers the operator request inbox and saved messages.
func isCommanderInboxPath(path string) bool {
	i := strings.Index(path, "/api/")
	if i < 0 {
		return false
	}
	seg, _, _ := strings.Cut(path[i+len("/api/"):], "/")
	return seg == "operator-requests" || seg == "instructions"
}

// isStatePath reports whether path addresses the commander note. The note sits
// on the project mux (one base, one token) but is not battlefield data: the gate
// serves it only to the commander identity.
func isStatePath(path string) bool {
	return strings.HasPrefix(path, "/p/") && strings.HasSuffix(path, "/api/state")
}

// hostEnv is the environment the local harness host starts with: the model's
// own variables, the Python UTF-8 defaults, and the path to the session shell
// init. No credential is placed here: every session on a host inherits this
// environment, so a token in it belongs to the executors too. The init script
// exports a per-session token/base from the caller's own case at shell start;
// the operator token is dropped from the child env as well (D109).
func hostEnv(modelEnv []string, sessionEnv string) []string {
	out := append([]string{}, modelEnv...)
	out = append(out, aop.DisableNativeIOASendEnv+"=1")
	if path := strings.TrimSpace(sessionEnv); path != "" {
		// Git Bash on Windows accepts forward slashes; a backslash path would be
		// read as escapes.
		path = filepath.ToSlash(path)
		out = append(out, "BASH_ENV="+path, "ENV="+path)
	}
	// Windows toolchains default to the ANSI code page (cp936 on the hosts this
	// console runs on): a Python remediation script that opens a UTF-8 JSON file
	// without an explicit encoding decodes as GBK and dies on the first Chinese
	// byte. Python's UTF-8 mode is the supported switch, and every harness
	// session inherits it, so a run does not rediscover this per command. An
	// explicit value from the operator's model env wins.
	out = withDefaultEnv(out, "PYTHONUTF8", "1")
	out = withDefaultEnv(out, "PYTHONIOENCODING", "utf-8")
	return out
}

// withDefaultEnv appends key=value unless the caller already set that key.
func withDefaultEnv(env []string, key, value string) []string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return env
		}
	}
	return append(env, prefix+value)
}

// Remote scans reconnect after a control-plane restart. Derive a separate Hub
// credential without persisting another shell secret in the shared data tree.
func hubAccessToken(consoleToken, dataDir string) string {
	mac := hmac.New(sha256.New, []byte(consoleToken))
	_, _ = mac.Write([]byte("saker/harness-hub/v1\x00" + filepath.Clean(dataDir)))
	return hex.EncodeToString(mac.Sum(nil))
}

// sessionWriteRoutes is the complete set of non-GET routes a harness session may
// reach on the project base, keyed by method and a normalized path ("*" is one
// path segment). It is an allowlist on purpose: a write handler added later stays
// 404 for sessions until someone lists it here, instead of becoming reachable
// because it was missing from an older denial list. Executor writes are absent by
// design — they go through /agent/, where each handler scopes them to the
// caller's own intent. The API handlers still re-check provenance where the
// route permits a commander to act for the operator (goals), so this is a
// visibility boundary, not the only check.
var sessionWriteRoutes = map[string]bool{
	"POST /api/instructions/*/ack": true, // acknowledge an instruction
	"POST /api/intents":            true, // open a direction
	"POST /api/intents/*/run":      true,
	"POST /api/intents/*/handoff":  true,
	"POST /api/intents/*/steer":    true,
	"POST /api/intents/*/say":      true,
	"POST /api/intents/*/park":     true,
	"POST /api/intents/*/cancel":   true,
	"POST /api/intents/*/retry":    true,
	"POST /api/intents/*/reopen":   true, // bring a parked direction back once its blocker is gone
	"POST /api/intents/*/settle":   true, // declare a stopped/failed direction done, on cited evidence
	"POST /api/intents/*/continue": true,
	"POST /api/intents/*/operator": true, // ask the operator for help; /complete stays operator-only
	"PUT /api/intents/*/context":   true, // adjust a direction's references
	"POST /api/context/ack":        true, // context-pressure receipt
	"POST /api/retrieval/index":    true,
	"POST /api/goals":              true, // only with an operator message (handler re-checks)
	"POST /api/goals/*/update":     true, // provenance re-checked by the handler
	"POST /api/goals/*/close":      true,
	"POST /api/goals/*/complete":   true,
	"POST /api/goals/*/reopen":     true,
	"POST /api/playbooks/nominate": true,
	"POST /api/messages":           true, // say / send to the operator or a peer
	"POST /api/messages/*/retract": true,
	"PUT /api/state":               true, // the commander's note
	// Proposals: an executor files them on /agent; the commander decides them
	// here. Approve/reject/dispatch are named explicitly; creating and
	// withdrawing stay participant-only routes.
	"POST /api/proposals/*/approve":  true,
	"POST /api/proposals/*/reject":   true,
	"POST /api/proposals/*/dispatch": true,
	"POST /api/project/standby":      true, // the commander's own stop request
	"POST /api/operator-requests":    true, // register a request; it is not a grant
	"PUT /api/operator-requests/*":   true,
}

func sessionWriteAllowed(method, path string) bool {
	return sessionWriteRoutes[method+" "+normalizeSessionPath(path)]
}

// goalWriteCarriesRunIdentity reports whether a write must reach its handler
// with the commander's run identity attached. Two families need it: goal
// definitions (update/close re-check operator provenance against the live
// round) and stamp writes (complete records MetByRun; reopen decides whether the
// session may withdraw the stamp at all). Without the identity the handlers see
// only "a session" and cannot tell the commander's judgement from the operator's.
func goalWriteCarriesRunIdentity(method, path string) bool {
	if method == http.MethodGet {
		return false
	}
	if strings.HasSuffix(path, "/api/goals") {
		return true
	}
	if !strings.Contains(path, "/api/goals/") {
		return false
	}
	return strings.HasSuffix(path, "/update") || strings.HasSuffix(path, "/close") ||
		strings.HasSuffix(path, "/complete") || strings.HasSuffix(path, "/reopen")
}

// normalizeSessionPath replaces the identifying segment of the write routes above
// with "*" so one entry covers every id. Other paths are returned unchanged: they
// cannot match an allowlist entry anyway.
func normalizeSessionPath(path string) string {
	i := strings.Index(path, "/api/")
	if i < 0 {
		return path
	}
	segs := strings.Split(strings.Trim(path[i:], "/"), "/") // api, ...
	if len(segs) < 2 {
		return path
	}
	switch segs[1] {
	case "instructions":
		if len(segs) == 4 && segs[3] == "ack" {
			segs[2] = "*"
		}
	case "intents":
		if len(segs) == 4 {
			segs[2] = "*"
		}
	case "goals":
		if len(segs) == 4 {
			segs[2] = "*"
		}
	case "messages":
		if len(segs) == 4 && segs[3] == "retract" {
			segs[2] = "*"
		}
	case "proposals":
		if len(segs) == 4 {
			segs[2] = "*"
		}
	case "operator-requests":
		if len(segs) == 3 {
			segs[2] = "*"
		}
	}
	return "/" + strings.Join(segs, "/")
}

// agentRouteHidden reports whether a route is absent from the agent's API view.
// A harness session (commander or executor) sees only the battlefield: board,
// intents, facts, evidence, goals, playbooks, runs, messages, participants,
// retrieval/context, the knowledge base and the model tiers it may pick.
// Everything else — settings, projects, reports, review triggers, worker controls, imports,
// bundles, fleet/version, the decider/project switches — is operator-only and
// returns 404, so the agent neither sees it nor learns it exists (D109).
//
// The commander note is a deliberate exception: /api/state is hidden here by
// default, and the gate serves it only to the commander's per-session identity
// (wrap → isStatePath). An executor gets the same 404 it would for a missing
// route, so the note is not part of its read surface.
//
// 404, not 403: a refusal would confirm that a hidden surface exists, which is
// exactly the learning the boundary prevents (D102). This is a surface boundary
// for a trusted internal session, not a sandbox (D78).
//
// Scope: this answers "which segments of the project face (/p/<id>/api/...) stay
// visible to an agent identity" — a negative list, hidden by default. It is not
// the participant route table: what exists under /agent/<p>/api is
// internal/api/agent.go registerAgent, and how that face is mounted and
// authenticated is cmd/saker/hub.go serveAgent. The two sets differ on purpose:
// The project board is only on this surface.
func agentRouteHidden(path string) bool {
	// World publication/export is instance content management. Project sessions
	// nominate through their own authenticated API, not the operator's routes.
	if strings.HasPrefix(path, "/api/kb/") {
		return true
	}
	i := strings.Index(path, "/api/")
	if i < 0 {
		return false
	}
	rest := path[i+len("/api/"):]
	// Manual world-knowledge nomination and experience archiving are operator curation.
	if parts := strings.Split(rest, "/"); len(parts) == 3 && parts[0] == "playbooks" && (parts[2] == "kb-candidate" || parts[2] == "archive") {
		return true
	}
	if strings.HasSuffix(rest, "/operator/complete") {
		return true
	}
	// The commander's own global stop request is the one project switch a harness
	// session may reach: a round that judges continuing is wrong must be able to
	// stop the campaign, not just park a direction and wait to be woken for it.
	// It is a pause the operator alone can undo (/api/project/pause, resume and
	// append-goal stay hidden). This gate only admits the path: the handler still
	// requires a commander attribution (X-Saker-Actor / participant identity) and a
	// live commander round, and that is a declarative boundary for a trusted
	// internal session, not a sandbox (D78).
	if rest == "project/standby" {
		return false
	}
	seg := rest
	if strings.HasPrefix(rest, "traffic/") {
		return rest != "traffic/flows" && rest != "traffic/flow"
	}
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		seg = rest[:j]
	}
	switch seg {
	case "instructions":
		return false // Read and ACK are available; operator writes are gated by method below.
	case "changes", "context", "experience", "focus", "resources", "communication":
		return false
	case "graph":
		return rest != "graph" // Full-project export remains an operator action.
	case "retrieval":
		return rest != "retrieval/status" && rest != "retrieval/index"
	case "board", "intents", "facts", "evidence", "goals", "playbooks",
		"runs", "messages", "participants", "kb", "access", "artifacts", "operator-requests", "actions", "proposals":
		// state is absent by default: it is the commander's private memory, and
		// the gate serves it only to the commander identity (wrap → isStatePath).
		// An executor sees the same 404 it would for a missing route.
		// A fact carries its review history inside the fact body (readable), but
		// starting a review is an operator click: hide only the trigger routes.
		if seg != "facts" {
			return false
		}
		if rest == "facts/verify-pending" {
			return true
		}
		parts := strings.Split(rest, "/")
		return len(parts) == 3 && (parts[2] == "verify" || parts[2] == "review")
	default:
		return true
	}
}

// verifierReadAllowed is the complete read surface of a review run: the fact it
// must re-prove, the evidence that fact cites, and the events of a run. An
// allowlist for the same reason sessionWriteRoutes is one — a route added later
// stays absent from this role until someone names it here on purpose, while a
// denial list would let every new read route through.
//
// Deliberately absent, each for a stated reason:
//   - /api/reviews: another reviewer's verdict. Seeing it turns independent
//     recomputation into agreement, which is the one thing this role exists to
//     prevent.
//   - /api/board, /api/intents, /api/goals: the direction's situation, i.e. what
//     the party under review believes. A reviewer that reads it starts looking
//     for new evidence instead of re-proving the claim it was handed.
//   - /api/messages, /agent/: speaking, in any direction.
//   - every non-GET: a review is a verdict, and the verdict is written to
//     review.json in its own work dir, not to the ledger.
func verifierReadAllowed(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	// Only the project base. Instance routes are the operator's, and requiring the
	// prefix keeps a future instance-level route from becoming readable just
	// because it happens to share a name with one of these.
	if !strings.HasPrefix(path, "/p/") {
		return false
	}
	i := strings.Index(path, "/api/")
	if i < 0 {
		return false
	}
	parts := strings.Split(strings.Trim(path[i+len("/api/"):], "/"), "/")
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "facts":
		// The list is what makes "read the fact by id" possible without guessing
		// ids; the fact body carries its review history. "verify-pending" is the
		// operator's review trigger (POST only) and not a fact id: letting its
		// path shape through would answer 405, which confirms the route exists —
		// the one signal this role must never receive.
		if len(parts) == 2 && parts[1] == "verify-pending" {
			return false
		}
		return len(parts) == 1 || len(parts) == 2 ||
			(len(parts) == 3 && parts[2] == "history")
	case "evidence":
		return len(parts) == 1 || len(parts) == 2
	case "runs":
		return len(parts) == 3 && parts[2] == "events"
	}
	return false
}

// wrap gates every route except the login page, its assets and the liveness
// probe. API clients get a 401 they can read; browsers get the login form.
func (a *consoleAuth) wrap(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.loginSubmit)
	mux.HandleFunc("GET /logout", a.logout)
	// /healthz belongs to the instance, not to the login gate: a probe must not
	// depend on whether a token happens to be configured.
	mux.Handle("/healthz", next)
	mux.Handle("/static/", next)
	// Participant calls authenticate with a per-run collab token, not the
	// operator's console token. The hub validates that token and the project
	// binding before any handler runs; passing the path through here is not an
	// authentication decision.
	mux.Handle("/agent/", next)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if role, scope, ok := a.authorize(r); ok {
			ip := clientIP(r)
			a.limiter.reset(ip)
			a.bearer.reset(ip)
			if role == roleVerifier {
				// A review credential is a read surface and nothing else, on the
				// project that issued it. Everything outside verifierReadAllowed is
				// absent (404), not forbidden (403): a refusal would confirm that
				// /api/reviews or a write endpoint exists, and the whole point of
				// this role is that a reviewer cannot see them to be tempted
				// (D102/D109).
				if named := pathProject(r.URL.Path); named != "" && named != scope.project {
					http.NotFound(w, r)
					return
				}
				if !verifierReadAllowed(r.Method, r.URL.Path) {
					http.NotFound(w, r)
					return
				}
				// No identity rides the request: the handlers narrow a read by
				// direction when one is present, and a reviewer reads originals
				// (the fact, its evidence, what a run actually did) rather than
				// speaking for the direction that declared them.
				//
				// The surface marker does ride it: report/verifier runs stay absent
				// from every session's view, and a reviewer must not read the
				// previous review's journal either — seeing another reviewer's
				// reasoning is the one thing this role is built to prevent.
				r = r.WithContext(api.WithReviewerSurface(r.Context()))
				next.ServeHTTP(w, r)
				return
			}
			if role == roleAgent {
				// A session credential is scoped to one project. A commander
				// holds the token of its own project only, so a URL naming
				// another project is as absent as a route it may not see.
				if named := pathProject(r.URL.Path); scope.project != "" && named != "" && named != scope.project {
					http.NotFound(w, r)
					return
				}
				if !scope.commander && isCommanderInboxPath(r.URL.Path) {
					http.NotFound(w, r)
					return
				}
				// Visibility is a property of the surface, not a per-handler
				// afterthought: reads are the battlefield by default (minus the
				// hidden operator surfaces), while every write is denied unless the
				// route is named in sessionWriteAllowed. A write endpoint added
				// later is therefore invisible to a session until someone lists it
				// here on purpose — the previous negative conditions let every new
				// route through because it was not in an old denial list.
				if r.Method == http.MethodGet {
					hidden := agentRouteHidden(r.URL.Path)
					if isStatePath(r.URL.Path) {
						hidden = !scope.commander
					}
					if hidden {
						http.NotFound(w, r)
						return
					}
				} else if !scope.commander || !sessionWriteAllowed(r.Method, r.URL.Path) {
					// Executors never write on this base: their writes go through
					// /agent/, where the handler scopes them to the caller's intent.
					// Operator-only surfaces stay absent rather than forbidden (403
					// would confirm they exist, D102/D109).
					http.NotFound(w, r)
					return
				}
				// Writes through the project mux record their author from
				// X-Saker-Actor, so the commander's attribution is stamped from the
				// credential instead of relying on the model to volunteer a header
				// it can forget — a say without the header used to land as
				// `operator`. Executors never write here, so they are not stamped.
				// The header stays a claim about the speaker, not a second
				// permission (D86); the surface boundary above is unchanged.
				if scope.commander {
					r.Header.Set("X-Saker-Actor", collab.CommanderID)
					// The commander identity is attached only to the writes whose
					// handler needs the authenticated run: the note receipt, the
					// goal-definition provenance check, and sending to the operator
					// inbox. Other project reads stay unscoped: the commander
					// coordinates all directions rather than acting for one executor.
					goalDefinition := goalWriteCarriesRunIdentity(r.Method, r.URL.Path)
					if isStatePath(r.URL.Path) || goalDefinition || r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/messages") {
						r = r.WithContext(api.WithIdentity(r.Context(), scope.identity))
					}
				} else if scope.identity.IntentID != "" {
					// An executor's identity rides the request so the handlers
					// that scope a read by direction keep doing so on this base.
					// Without it, /p/<id>/api/runs/{other}/output, /context,
					// /events and the browser material endpoint all fail open:
					// they only narrow when identityOf succeeds. The commander is
					// deliberately not stamped — it coordinates the whole
					// project and has no single intent to be scoped to.
					r = r.WithContext(api.WithIdentity(r.Context(), scope.identity))
				}
				// The project mux's request still needs the session marker: the
				// API handlers use it to keep operator-side support runs and the
				// review queue out of every harness session's view.
				r = r.WithContext(api.WithAgentSurface(r.Context()))
			}
			next.ServeHTTP(w, r)
			return
		}
		// A wrong bearer is a retry loop, not a password guess at the form, so it
		// is throttled in its own bucket. A cookie that fails the HMAC is not a
		// guess an attacker can profit from — forging one needs the console
		// token — and counting it meant a token rotation or an expired session
		// locked the operator out of /login for the rest of the window, because
		// every console request carries the stale cookie.
		if attempt := strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer "); attempt {
			ip := clientIP(r)
			if retry := a.bearer.retryAfter(ip, time.Now()); retry > 0 {
				a.throttled(w, retry, wantsJSON(r))
				return
			}
			a.bearer.fail(ip, time.Now())
		}
		if wantsJSON(r) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			msg := "需要登录：打开 /login 输入控制台令牌"
			// A harness session's credential can be replaced between rounds. A
			// generic "please log in" would send the model hunting for a cookie it
			// cannot have; name the variable it already owns instead.
			if strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
				msg = "Bearer 无效或已过期：用 -H \"$SAKER_AUTH\" 重试（脚本里可用 -H \"$(cat .saker/auth)\"）；操作员请用控制台令牌"
			}
			_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
			return
		}
		target := "/login"
		if r.Method == http.MethodGet && r.URL.Path != "/" {
			target += "?next=" + urlEscape(r.URL.RequestURI())
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
	return mux
}

func wantsJSON(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.Contains(r.URL.Path, "/api/") {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func (a *consoleAuth) loginPage(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(renderLogin(next, "")))
}

func (a *consoleAuth) loginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if retry := a.limiter.retryAfter(ip, time.Now()); retry > 0 {
		a.throttled(w, retry, false)
		return
	}
	// A login token is a short string; a form bigger than a few KiB was not
	// filled in by a browser. Cap before ParseForm so its 10 MiB stdlib
	// allowance is not this unauthenticated endpoint's budget.
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单读不出来", http.StatusBadRequest)
		return
	}
	next := safeNext(r.FormValue("next"))
	got := strings.TrimSpace(r.FormValue("token"))
	want := string(a.key)
	// The cookie key is a hash of the token; compare the hash of what was typed
	// against it, so the plaintext token never has to be kept around.
	sum := sha256.Sum256([]byte(got))
	if subtle.ConstantTimeCompare(sum[:], []byte(want)) != 1 {
		a.limiter.fail(ip, time.Now())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(renderLogin(next, `<div class="err">令牌不对</div>`)))
		return
	}
	a.limiter.reset(ip)
	a.issue(w)
	http.Redirect(w, r, next, http.StatusFound)
}

func (a *consoleAuth) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: consoleCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// safeNext accepts only an absolute path on this console. Anything else (an
// absolute URL, a scheme-relative //host, a control byte that could split the
// Location header) becomes "/". The value is still HTML-escaped at render time:
// this check is about the redirect, not about the form.
func safeNext(next string) string {
	if next == "" || strings.HasPrefix(next, "//") {
		return "/"
	}
	if strings.ContainsAny(next, "\\\r\n\x00") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}
	return next
}

func urlEscape(s string) string {
	return strings.NewReplacer("%", "%25", " ", "%20", "?", "%3F", "&", "%26", "#", "%23").Replace(s)
}

// withSecurityHeaders wraps the whole console, including the gate: a 401, a 429
// and the login page get the same headers as the app. No CSP: the login page and
// the SPA both rely on inline styles, and a policy loose enough for them would
// not buy much.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		next.ServeHTTP(w, r)
	})
}

// withConsoleAuth wraps the hub. There is no "open" mode: a control plane that
// dispatches offensive work is never unauthenticated. sessions is the collab
// registry: the commander's per-session identity and an executor's run
// identity are the harness-session credentials the project mux accepts.
func withConsoleAuth(next http.Handler, token string, sessions sessionResolver) http.Handler {
	auth := newConsoleAuth(token, sessions)
	if auth == nil {
		log.Fatal("控制台令牌为空：拒绝以未授权方式启动")
	}
	log.Printf("控制台登录：已启用（令牌 %d 字节，会话 %s）", len(token), consoleTTL)
	return auth.wrap(next)
}

// ensureConsoleToken resolves the token from the flag/file/env, generating and
// persisting one when the operator did not supply any. The file is the source a
// restart reads, so the login survives a restart without repeating the flag.
//
// It lives under the data directory: the console token is an instance artifact,
// and the deployment contract keeps every instance artifact in one place that is
// backed up and access-controlled together. The trade-off is explicit in
// SECURITY.md — the host runs from the data directory, so an executor's file
// tools can reach a token stored here; operators who need it outside that root
// point --console-token-file at another location.
func ensureConsoleToken(dataDir, inline, file string) (token, path string, generated bool, err error) {
	t, err := resolveConsoleToken(inline, file)
	if err != nil {
		return "", "", false, err
	}
	if t != "" {
		return t, "", false, nil
	}
	// The generated token is the instance's identity: reuse it, or every
	// restart logs the operator out and invalidates copied URLs.
	path = filepath.Join(dataDir, "console.token")
	if b, err := os.ReadFile(path); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v, path, false, nil
		}
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", "", false, err
	}
	t = hex.EncodeToString(buf)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", "", false, err
	}
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", "", false, err
	}
	return t, path, true, nil
}

// resolveConsoleToken reads --console-token, then --console-token-file, then
// SAKER_CONSOLE_TOKEN. The file variant exists so the secret does not show up in a
// process listing or a shell history.
func resolveConsoleToken(inline, file string) (string, error) {
	if v := strings.TrimSpace(inline); v != "" {
		return v, nil
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	return strings.TrimSpace(os.Getenv("SAKER_CONSOLE_TOKEN")), nil
}

// loginHTML is the one page the console shell does not draw, so it carries its
// own copy of the wordmark: the same point/stroke strings as
// web/src/components/Wordmark.tsx and web/public/favicon.svg. web/tests/
// brand-mark.test.ts reads this file and fails when the three copies drift.
const loginHTML = `<!doctype html>
<html lang="zh-CN" class="dark"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="theme-color" content="#0a0a0b">
<title>Saker · 登录</title>
<link rel="icon" type="image/svg+xml" href="/static/favicon.svg">
<style>
:root{--bg:#0a0a0b;--card:#121215;--border:#26262b;--fg:#e9e9ee;--muted:#8b8b93;--accent:#ff4d4d}
*{box-sizing:border-box}
body{margin:0;height:100vh;display:grid;place-items:center;background:
radial-gradient(700px 400px at 30% -10%,rgba(255,77,77,.08),transparent),var(--bg);
color:var(--fg);font:14px/1.65 "Inter","PingFang SC","Microsoft YaHei",system-ui,sans-serif}
.card{width:340px;border:1px solid var(--border);border-radius:10px;background:var(--card);padding:22px}
.brand{display:flex;flex-direction:column;gap:8px;margin-bottom:20px}
.wordmark{color:#eeeae1;display:block}
.sub{color:var(--muted);font-size:9px;font-weight:600;letter-spacing:.2em;text-transform:uppercase}
label{display:block;color:var(--muted);font-size:11px;margin-bottom:6px}
input{width:100%;height:34px;padding:0 10px;border-radius:6px;border:1px solid var(--border);
background:#0d0d10;color:var(--fg);font:inherit;font-family:ui-monospace,monospace}
input:focus{outline:1px solid var(--accent);border-color:var(--accent)}
button{margin-top:14px;width:100%;height:34px;border:0;border-radius:6px;background:var(--accent);
color:#170303;font:inherit;font-weight:700;cursor:pointer}
.err{color:var(--accent);font-size:11px;margin-top:10px}
.hint{color:var(--muted);font-size:10px;margin-top:12px}
</style></head><body>
<form class="card" method="post" action="/login">
  <div class="brand">
    <svg class="wordmark" viewBox="0 0 367 100" width="210" height="57" role="img" aria-label="Saker">
      <g fill="currentColor" stroke="currentColor">
        <g transform="translate(0 0)"><clipPath id="wm-s"><rect x="0" y="0" width="66" height="100"/></clipPath><g clip-path="url(#wm-s)"><path d="M58 22 C58 11 47 8 33 8 C18 8 8 14 8 26 C8 37 16 43 30 48 C45 53 55 59 55 71 C55 84 44 92 29 92 C14 92 8 86 8 74" fill="none" stroke-width="16" stroke-linecap="butt" stroke-linejoin="miter" stroke-miterlimit="10"/></g></g>
        <g transform="translate(74 0)"><path d="M0 100 L26 0 L42 0 L68 100 L52 100 L46.24 76 L21.76 76 L16 100 Z M34 25 L25.6 60 L42.4 60 Z" fill-rule="evenodd"/></g>
        <g transform="translate(153 0)"><clipPath id="wm-k"><rect x="0" y="0" width="68" height="100"/></clipPath><g clip-path="url(#wm-k)"><path d="M8 0 L8 100 M72 0 L16 52 L72 100" fill="none" stroke-width="16" stroke-linecap="butt" stroke-linejoin="miter" stroke-miterlimit="10"/></g></g>
        <g transform="translate(233 0)"><clipPath id="wm-e"><rect x="0" y="0" width="56" height="100"/></clipPath><g clip-path="url(#wm-e)"><path d="M8 0 L8 100 M0 8 L56 8 M0 50 L48 50 M0 92 L56 92" fill="none" stroke-width="16" stroke-linecap="butt" stroke-linejoin="miter" stroke-miterlimit="10"/></g></g>
        <g transform="translate(301 0)"><clipPath id="wm-r"><rect x="0" y="0" width="66" height="100"/></clipPath><g clip-path="url(#wm-r)"><path d="M8 0 L8 100 M8 8 L32 8 C52 8 58 17 58 30 C58 44 50 52 32 52 L8 52 M34 56 L66 104" fill="none" stroke-width="16" stroke-linecap="butt" stroke-linejoin="miter" stroke-miterlimit="10"/></g></g>
      </g>
    </svg>
    <div class="sub">授权安全测试控制台</div>
  </div>
  <input type="hidden" name="next" value="{{next}}">
  <label for="token">控制台令牌</label>
  <input id="token" name="token" type="password" autofocus autocomplete="current-password"
         placeholder="启动时 --console-token 的值">
  <button type="submit">进入</button>
  {{err}}
  <div class="hint">令牌由 <code>--console-token</code> / <code>--console-token-file</code> / <code>SAKER_CONSOLE_TOKEN</code> 指定；自动生成时写入 &lt;data&gt;/console.token（启动日志只打印路径，不打印令牌）。会话 12 小时，退出访问 /logout。</div>
</form></body></html>`

// renderLogin fills the two placeholders; formatting the page with Printf would
// mean escaping every percent in the CSS, which is how the first version broke.
//
// next is operator input from the URL/form and lands inside a value="…"
// attribute, so it is HTML-escaped. Without this a crafted /login?next= URL ran
// script on the console origin, where the session cookie already lives.
func renderLogin(next, errBlock string) string {
	return strings.NewReplacer(
		"{{next}}", html.EscapeString(next),
		"{{err}}", errBlock,
	).Replace(loginHTML)
}
