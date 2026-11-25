package decider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/board"
	"github.com/kksty/tianwu/internal/model"
	"github.com/kksty/tianwu/internal/retrieval"
	"github.com/kksty/tianwu/internal/state"
	"github.com/kksty/tianwu/internal/store"
)

// Runner owns the wake/debounce loop and writes every round to the decision
// log. A round is one harness commander session (D78): this type schedules it,
// gates it, and records the result; it never calls a model itself.
type Runner struct {
	Store *store.Store
	State *state.File
	// ModelName is the configured model, shown on the console's commander card.
	// The actual client lives in the harness host, not here.
	ModelName string
	// ContextWindow is the model's context budget. It turns the session's
	// reported context size into the 85% handoff line; 0 disables the pressure
	// section (the harness still compacts, the prompt just cannot say when).
	ContextWindow int
	Scope         string
	Debounce      time.Duration
	Heartbeat     time.Duration
	// RoundTimeout caps one commander round's wall clock. A round is a
	// decision: while it runs, every wake it would answer is queued behind it
	// (the loop calls Commander synchronously), so a round that never ends
	// stalls decisions no matter how healthy the rest of the campaign is.
	// 0 disables the cap. A cut round is canceled with ErrRoundTimeout, not
	// failed: its actions already count, and the next wake decides again on a
	// fresh board with the same session (D91).
	RoundTimeout time.Duration
	// OnWake reports one commander round's reasons to the Phase 1 sidecar. It
	// runs before the model call and must not block; nil disables it.
	OnWake  func(reasons []string, heartbeat bool, promptBytes int)
	LogPath string
	// ReviewsOff reports the instance-level review switch. The commander prompt
	// and the board projection use it: with review disabled there is no verdict
	// to wait for, so unverified fact text is shown and may seed directions.
	ReviewsOff func() bool
	// DispatchReady gates rounds before spending tokens, preserving the actual
	// execution/model failure for the operator. nil disables the gate.
	DispatchReady func() error
	// Commander runs one round in the harness. It returns the round's token
	// total so the decision log records token consumption; resume asks to continue
	// the project's existing session (the caller resolves it).
	Commander func(ctx context.Context, round string, resume bool) (RoundResult, error)
	// Session tells the runner whether this round continues the harness session
	// (resume/restart). It is resolved by the caller because only the executor
	// can answer CanResume; nil means the zero RoundContext, which injects the
	// note in full (the safe side). Set before Start.
	Session func() RoundContext
	// Settled is called once the project has nothing left to attend to: every
	// goal is met and no intent is still waiting on the decider. The hub uses it
	// to pause the project automatically, so a finished engagement stops
	// spending heartbeats and the console shows the paused state instead of
	// pretending it is still working. It runs on the runner goroutine and must
	// not block.
	Settled func()

	mu              sync.Mutex
	runMu           sync.Mutex
	cancelRound     context.CancelCauseFunc
	round           int
	last            *Decision
	lastSupervision map[string]supervisionMark
	tokens          int
	// stalled is the durable consecutive no-progress count. The store owns it
	// (Store.ObserveDecisionRound); this copy is initialized at start and kept
	// current after each counted round so Status can show it. It survives a
	// restart because the store does, not because this process remembers.
	stalled int
	// lastContext / lastCompacted describe the previous commander round's
	// ending context. They are the one thing the model cannot see for itself,
	// so every round hands them back in the prompt (D80).
	lastContext   int
	lastCompacted bool
	// observedWindow is the window the harness resolved for the model (usage
	// detail). It wins over the profile's context_window: the harness knows the
	// model's real window, the control plane would only be guessing (D80).
	observedWindow int
	// handoffDone is "the note was rewritten in this pressure cycle". Without
	// it every round above 85% would be a handoff round and nothing would ever
	// be dispatched.
	handoffDone bool
	// handoffForced is "compaction happened before a verified handoff". It
	// survives the pressure reset and makes the next round a repair handoff even
	// when the post-compaction context is small.
	handoffForced   bool
	handoffMissing  int
	handoffBypassed bool
	noActionRounds  int
	// idleReplans counts the forced idle-replan rounds in the current idle
	// episode. It is bounded: the first round turns the generic request into a
	// concrete one (or dispatches), the second is the watchdog escalation after
	// the commander burned several no-action rounds. An unresponsive commander
	// must not be woken in a loop.
	idleReplans int
	// idleReplanAtNoAction is the no-action count when the first idle replan was
	// requested. The escalation measures progress from here, so a stale count
	// carried in from before the idle episode cannot trigger a second round
	// immediately after the first.
	idleReplanAtNoAction int
	// handoffRound is "the previous round was the handoff round". It is a
	// one-round flag, not a cycle flag: the full note is re-injected exactly
	// once after the handoff, and the ordinary pointer contract takes over from
	// the round after that. Overloading handoffDone (which stays true until
	// compaction) re-pasted the note on every pressure round and made the
	// prompt claim "the previous round was the handoff" long after it was.
	handoffRound bool
	// pendingRotate is the in-memory, one-shot decision "the next round starts a
	// new commander session". Rotation is built in: every handoff round that
	// writes a valid receipt closes its pressure cycle and the next round opens
	// a fresh session from the note, board and current constraints. Arming is
	// never re-derived from the persisted Decision (only from what this process
	// observed), it is consumed by exactly one round, and it is not retried
	// after a failed new-session round. A restart forgets it and keeps the long
	// session: the note is still the memory, and a fresh session can be started
	// by the next handoff.
	pendingRotate bool
	// now is the clock retry scheduling reads. nil means time.Now; tests
	// replace it to walk the heartbeat/backoff timeline without sleeping. It is
	// not a business clock and must not be used for decision timestamps.
	now     func() time.Time
	wake    chan string
	started bool
	// activity is what the console's live card shows: the phase of the round
	// in flight, not a guess reconstructed from the decision log.
	activity     Activity
	activityWake string
	// paused is the operator's kill switch: wakes are dropped and the active
	// round is canceled until an explicit Resume. selfPaused records that the
	// stop came from the commander's own round (StandbyFor): that round is not
	// canceled, so its answer and delivery checkpoint still belong to it.
	paused      bool
	selfPaused  bool
	pauseReason string
	startedAt   time.Time
	retryAfter  time.Time // transport recovery backoff; never a strategy/fee gate
	// idleRequested is the one-shot latch for the idle operator request. The
	// heartbeat writes one generic operator_request row when the goal is open
	// and no direction is in flight; the latch keeps it from writing the same
	// row every five minutes. It resets as soon as work appears or the goal
	// closes.
	idleRequested bool
}

// ErrRoundTimeout is the cancellation cause of a commander round cut by
// Runner.RoundTimeout. It is a scheduler decision, not the model's verdict and
// not a project stop: the control plane records the round as canceled, keeps
// the session, and lets the next wake decide again on the newest board (D91).
var ErrRoundTimeout = errors.New("本轮超过墙钟上限")

var ErrPaused = errors.New("操作员已暂停指挥官")

// ErrCommanderStalled is a commander round stopped by the model_stall liveness
// detector: the model kept sending empty replies and requesting the LLM again
// instead of calling a tool or ending the turn. It is neither a project stop
// nor a scheduler cut — the session survives — but the round made no progress,
// so it must reach the decider as the round's error instead of a normal
// completion. Otherwise Decision.Error stays empty and the console's commander
// card hides why the round ended (D107). Changing the tier is an operator
// action: the commander's long session cannot move hosts mid-flight (docs/17
// §3.5.1), so no automatic switch is faked here.
var ErrCommanderStalled = errors.New("指挥官模型连续空回复（model_stall）：本轮被存活检测取消")

func NewRunner(st *store.Store, mem *state.File, logPath string) *Runner {
	if mem != nil {
		mem.Bind(st)
	}
	r := &Runner{
		Store:        st,
		State:        mem,
		LogPath:      logPath,
		Debounce:     3 * time.Second,
		Heartbeat:    5 * time.Minute,
		RoundTimeout: 15 * time.Minute,
		wake:         make(chan string, 1),
	}
	if st != nil {
		// Continue a stall that began before this process started.
		r.stalled = st.StallRounds()
	}
	return r
}

// SetModelName updates the console-visible model when the settings page saves.
func (r *Runner) SetModelName(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.ModelName = name
	r.mu.Unlock()
}

func (r *Runner) SetScope(scope string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.Scope = scope
	r.mu.Unlock()
}

// SetContextWindow follows the settings page: the handoff line moves with the
// model profile, so a window change must not wait for a process restart. The
// harness-reported window is dropped with it: the next round reports the window
// of the new profile.
func (r *Runner) SetContextWindow(n int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.ContextWindow = n
	r.observedWindow = 0
	r.mu.Unlock()
}

// SetRoundTimeout follows the settings page's per-project override: the cap a
// commander round runs under changes from the next round on, without a restart.
// 0 disables the cap (D91).
func (r *Runner) SetRoundTimeout(d time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.RoundTimeout = d
	r.mu.Unlock()
}

// Status is what the console shows about the decider itself.
type Status struct {
	Enabled            bool                    `json:"enabled"`
	Model              string                  `json:"model,omitempty"`
	Rounds             int                     `json:"rounds"`
	Tokens             int                     `json:"tokens_total"`
	Stalled            int                     `json:"stalled"`
	ContextTokens      int                     `json:"context_tokens,omitempty"`
	ContextWindow      int                     `json:"context_window,omitempty"`
	ContextPercent     int                     `json:"context_percent,omitempty"`
	Compacted          bool                    `json:"compacted,omitempty"`
	HandoffMissing     int                     `json:"handoff_missing,omitempty"`
	HandoffReleased    bool                    `json:"handoff_released,omitempty"`
	NoActionRounds     int                     `json:"no_action_rounds,omitempty"`
	LastRoundAt        *time.Time              `json:"last_round_at,omitempty"`
	LastWake           string                  `json:"last_wake,omitempty"`
	LastDigest         string                  `json:"last_digest,omitempty"`
	LastError          string                  `json:"last_error,omitempty"`
	LastFallback       string                  `json:"last_fallback,omitempty"`
	LastActions        []Action                `json:"last_actions,omitempty"`
	LastNotes          []string                `json:"last_notes,omitempty"`
	LastAttention      []store.AttentionReason `json:"last_attention,omitempty"`
	PendingAttention   []store.AttentionReason `json:"pending_attention,omitempty"`
	AttentionDelivered bool                    `json:"attention_delivered,omitempty"`
	// Activity is the live phase of the in-flight round. It is present even
	// when the decider is idle: the card then says what it is waiting for.
	Activity Activity `json:"activity"`
	// HeartbeatSeconds is the idle cadence, so the card can say how long it
	// will stay quiet rather than showing a frozen 0.
	HeartbeatSeconds int `json:"heartbeat_seconds,omitempty"`
	// Paused is the operator's kill switch; UptimeSeconds answers "how long has
	// this been running unattended".
	Paused        bool   `json:"paused,omitempty"`
	PausedReason  string `json:"paused_reason,omitempty"`
	UptimeSeconds int    `json:"uptime_seconds,omitempty"`
	// RunTokensHour is what runs spent in the last hour (intent runs and
	// commander rounds alike). Recorded for the operator, never enforced.
	RunTokensHour int `json:"run_tokens_last_hour,omitempty"`
	// SessionRounds / SessionDropped belonged to the local decision cache (D56,
	// retired by D78). Kept because the console's bundle reads them; always 0.
	SessionRounds  int `json:"session_rounds,omitempty"`
	SessionDropped int `json:"session_dropped,omitempty"`
}

// Activity is the "what is it doing right now" record behind the graph's live
// card: phase, current round, the wake that started it and one short detail.
type Activity struct {
	Phase  string     `json:"phase"`
	Round  int        `json:"round,omitempty"`
	Wake   string     `json:"wake,omitempty"`
	Detail string     `json:"detail,omitempty"`
	Tool   string     `json:"tool,omitempty"`
	Since  *time.Time `json:"since,omitempty"`
}

// setActivity records a phase change. Re-setting the same phase and detail is a
// no-op, so the bar does not restart on every heartbeat.
func (r *Runner) setActivity(phase, detail string) {
	if r == nil {
		return
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused && phase != "paused" {
		return
	}
	if r.activity.Phase == phase && r.activity.Detail == detail {
		return
	}
	r.activity = Activity{Phase: phase, Round: r.round, Wake: r.activityWake, Detail: detail, Since: &now}
}

func (r *Runner) Status() Status {
	if r == nil {
		return Status{Enabled: false}
	}
	pending := store.AttentionBatch{}
	if r.Store != nil {
		pending = r.Store.CommanderAttention()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st := Status{
		Enabled:         r.ModelName != "" && r.Commander != nil,
		Model:           r.ModelName,
		Rounds:          r.round,
		Tokens:          r.tokens,
		Stalled:         r.stalled,
		ContextTokens:   r.lastContext,
		ContextWindow:   r.windowLocked(),
		ContextPercent:  (ContextStatus{Used: r.lastContext, Window: r.windowLocked()}).Percent(),
		Compacted:       r.lastCompacted,
		HandoffMissing:  r.handoffMissing,
		HandoffReleased: r.handoffBypassed,
		NoActionRounds:  r.noActionRounds,
		Paused:          r.paused,
		PausedReason:    r.pauseReason,
		Activity:        r.activity,
		RunTokensHour:   hourlyRunTokens(r.Store, time.Now().Add(-time.Hour)),
	}
	if r.Heartbeat > 0 {
		st.HeartbeatSeconds = int(r.Heartbeat / time.Second)
	}
	if !r.startedAt.IsZero() {
		st.UptimeSeconds = int(time.Since(r.startedAt).Seconds())
	}
	if r.last != nil {
		at := r.last.At
		st.LastRoundAt = &at
		st.LastWake = r.last.Wake
		st.LastDigest = r.last.Digest
		st.LastError = r.last.Error
		st.LastFallback = r.last.Fallback
		st.LastActions = r.last.Actions
		st.LastNotes = r.last.Notes
		st.LastAttention = append([]store.AttentionReason(nil), r.last.Attention...)
		st.AttentionDelivered = r.last.AttentionDelivered
	}
	st.PendingAttention = pending.Reasons
	return st
}

// Pause stops commander rounds without stopping the process. A long unattended
// run needs a kill switch that keeps the ledger, the watchdog and the console
// alive.
func (r *Runner) Pause() {
	r.PauseFor("操作员暂停了指挥官：等你点「继续」")
}

// PauseFor is Pause with a reason the console can show.
func (r *Runner) PauseFor(reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.paused = true
	r.selfPaused = false
	r.pauseReason = reason
	if r.cancelRound != nil {
		r.cancelRound(ErrPaused)
	}
	// Queued work is not permission to spend after a later resume. Resume's
	// explicit wake will read the then-current board instead.
drain:
	for {
		select {
		case <-r.wake:
		default:
			break drain
		}
	}
	r.mu.Unlock()
	if reason != "" {
		r.setActivity("paused", reason)
	}
}

// StandbyFor is the commander's own stop request: the wake loop pauses, but the
// round that asked for it is NOT canceled. PauseFor is the operator's kill
// switch — it cuts an in-flight round immediately because a human wants the
// spending to end now. A commander round that judges "this engagement should
// stop" must instead be allowed to finish its answer and its bookkeeping; the
// project pause it requested is already visible to the console while that
// happens (the API sets the dispatch gate before calling this).
//
// Queued wakes are dropped for the same reason PauseFor drops them: after a
// pause, only an explicit operator resume may buy the next round.
func (r *Runner) StandbyFor(reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.paused = true
	r.selfPaused = true
	r.pauseReason = reason
drain:
	for {
		select {
		case <-r.wake:
		default:
			break drain
		}
	}
	r.mu.Unlock()
	if reason != "" {
		r.setActivity("paused", reason)
	}
}

// Resume re-arms the loop; it is explicit, never automatic.
func (r *Runner) Resume() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.paused = false
	r.selfPaused = false
	r.retryAfter = time.Time{}
	r.pauseReason = ""
	r.idleRequested = false
	r.mu.Unlock()
	r.refreshIdleOperatorRequest()
}

// Paused reports the kill switch.
func (r *Runner) Paused() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}

// Wake asks for a round after the debounce window. Calls coalesce.
func (r *Runner) Wake(reason string) {
	if r == nil {
		return
	}
	if reason == "" {
		reason = "change"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return
	}
	select {
	case r.wake <- reason:
		return
	default:
	}
	if !isForced(reason) {
		return
	}
	// A forced wake (the operator asked for this round) must not be dropped just
	// because a normal change is already queued. Replace that pending reason;
	// if the queued one is forced too, keep it — either way the next round is
	// forced.
	select {
	case old := <-r.wake:
		if isForced(old) {
			select {
			case r.wake <- old:
			default:
			}
			return
		}
	default:
	}
	select {
	case r.wake <- reason:
	default:
	}
}

func (r *Runner) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.startedAt = time.Now()
	r.mu.Unlock()
	r.seedContext()
	r.setActivity("idle", "等待第一次唤醒")
	go r.loop(ctx)
}

// windowLocked is the window pressure is measured against: the harness's
// resolved value wins over the profile setting. Callers hold r.mu.
func (r *Runner) windowLocked() int {
	if r.observedWindow > 0 {
		return r.observedWindow
	}
	return r.ContextWindow
}

// clock is time.Now unless a test walks the retry timeline.
func (r *Runner) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// attention is the durable global demand as of the runner's clock. The retry
// schedule is the only time-dependent part of it.
func (r *Runner) attention() store.AttentionBatch {
	if r.Store == nil {
		return store.AttentionBatch{Reasons: []store.AttentionReason{}}
	}
	return r.Store.CommanderAttentionAt(r.clock())
}

// seedContext recovers the session's pressure across a process restart: the AOP
// session is resumed, so its harness context is not empty just because this
// Runner is new. What the resumed session holds is the size the harness reported
// at the end of the last round: when that round compacted, FinalContextTokens
// (post-compaction) is far below the peak ContextTokens, and the peak describes
// a context that no longer exists.
//
// Seeding the peak is not "one harmless extra handoff round": it makes the first
// round after a restart look like the 85% handoff, and a handoff round
// deliberately dispatches nothing. With no run in flight, no later event wakes
// the work it deferred, so an operator's explicit continue ends in an idle
// project with an open goal and nothing executing. The same is true of a round
// that ended at the peak because the pressure cycle had already closed with a
// verified handoff: the note is current, so re-running the handoff is pure waste.
// That cycle state is persisted on the round (model.Run.HandoffDone), so the
// restart restores the flag instead of re-arming the handoff.
func (r *Runner) seedContext() {
	if r.Store == nil {
		return
	}
	var latest model.Run
	found := false
	for _, run := range r.Store.Runs() {
		if run.Purpose != "commander" {
			continue
		}
		if !found || run.StartedAt.After(latest.StartedAt) {
			latest, found = run, true
		}
	}
	if !found {
		return
	}
	used := latest.FinalContextTokens
	if used <= 0 {
		// A round that reported no final size never compacted: its last known
		// size is the peak. 0 means unknown, not an empty session.
		used = latest.ContextTokens
	}
	r.mu.Lock()
	if r.lastContext == 0 {
		r.lastContext = used
	}
	if latest.ContextWindow > 0 {
		r.observedWindow = latest.ContextWindow
	}
	// A compaction (or a planned rotation) already reset the cycle when that
	// round settled, so the persisted state is the current cycle state.
	if latest.HandoffDone {
		r.handoffDone = true
	}
	r.mu.Unlock()
}

func (r *Runner) loop(ctx context.Context) {
	hb := r.Heartbeat
	if hb <= 0 {
		hb = 5 * time.Minute
	}
	tick := time.NewTicker(hb)
	defer tick.Stop()
	r.refreshIdleOperatorRequest()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if r.Paused() {
				continue
			}
			r.refreshIdleOperatorRequest()
			if r.needsAttention() && r.heartbeatWorthwhile() {
				r.Run(ctx, "heartbeat", false)
			} else {
				r.setActivity("idle", r.heartbeatIdleDetail())
			}
			r.settleIfDone()
		case reason := <-r.wake:
			r.setActivity("debounce", "去抖：等这一阵变化安静下来")
			reason, ok := r.debounce(ctx, reason)
			if !ok {
				return
			}
			r.Run(ctx, reason, isForced(reason))
			r.refreshIdleOperatorRequest()
			r.settleIfDone()
		}
	}
}

// refreshIdleOperatorRequest materialises the idle state as one structured
// fallback request, wakes exactly one forced replan round per idle episode, and
// closes the request when work resumes. The latch keeps a five-minute heartbeat
// from rewriting the same row or waking the commander again.
func (r *Runner) refreshIdleOperatorRequest() {
	if r.Store == nil || r.Paused() {
		return
	}
	if r.idleGoalNeedsOperator() {
		// The latch names one pending row, not the idle state itself. The
		// operator (or the idle round itself) can resolve or dismiss that row
		// while the project stays idle; keeping the latch set then hides every
		// recovery: no fallback in the inbox, no forced replan, and the card
		// still claims "waiting for the operator". Follow the row instead.
		if len(r.Store.PendingOperatorRequests()) == 0 {
			r.mu.Lock()
			r.idleRequested = false
			r.mu.Unlock()
		}
		r.mu.Lock()
		asked := r.idleRequested
		r.mu.Unlock()
		if asked {
			// The fallback exists and the one edge-triggered replan already ran.
			// The watchdog may grant one more round, but only after the
			// commander has repeatedly produced nothing.
			r.escalateIdleReplan()
			return
		}
		if _, created, err := r.Store.EnsureIdleOperatorRequest(); err != nil {
			r.setActivity("blocked", "写入操作员请求失败："+err.Error())
			return
		} else {
			r.mu.Lock()
			r.idleRequested = true
			r.mu.Unlock()
			if created {
				// Edge-triggered: exactly once per idle episode. The upcoming
				// round sees the pending request id and either fills it with a
				// concrete ask or dispatches a direction (which dismisses it).
				r.forceIdleReplan()
			}
		}
		return
	}
	r.mu.Lock()
	wasAsked := r.idleRequested
	r.idleRequested = false
	r.idleReplans = 0
	r.idleReplanAtNoAction = 0
	r.mu.Unlock()
	if wasAsked {
		_ = r.Store.DismissIdleOperatorRequests("work_resumed")
	}
}

// forceIdleReplan wakes one forced idle round, bounded per idle episode. The
// wake channel is buffered and coalesces, so a wake already queued is not a
// second round; the counter is what bounds the episode.
func (r *Runner) forceIdleReplan() {
	if r.Paused() {
		return
	}
	r.mu.Lock()
	if r.idleReplans >= maxIdleReplans {
		r.mu.Unlock()
		return
	}
	r.idleReplans++
	n := r.idleReplans
	if n == 1 {
		// The escalation counts no-action rounds that happen *after* this
		// replan; a count carried in from before the idle episode is history.
		r.idleReplanAtNoAction = r.noActionRounds
	}
	r.mu.Unlock()
	if n > 1 {
		r.setActivity("idle", "目标未达成且无在办意图：看门狗强制第 2 次重规划")
	}
	r.Wake("idle")
}

// escalateIdleReplan is the watchdog path. It only fires when the commander has
// several no-action rounds behind it and the episode has not already spent its
// replans. After that it stays quiet. It deliberately does not depend on the
// fallback row still being pending (or still generic): a row resolved in between
// would otherwise silence the last automatic recovery the project has, so when
// no row survives it files a fresh one before spending the round.
func (r *Runner) escalateIdleReplan() {
	r.mu.Lock()
	rounds, replans, base := r.noActionRounds, r.idleReplans, r.idleReplanAtNoAction
	r.mu.Unlock()
	if replans >= maxIdleReplans || rounds < 3 || rounds-base < 3 {
		return
	}
	if len(r.Store.PendingOperatorRequests()) == 0 {
		if _, _, err := r.Store.EnsureIdleOperatorRequest(); err != nil {
			r.setActivity("blocked", "写入操作员请求失败："+err.Error())
			return
		}
	}
	r.forceIdleReplan()
}

// wakeHandoffContinue is the automatic follow-up wake after a verified handoff
// round. A handoff round deliberately dispatches nothing: it rewrites the note
// and ends. With no run in flight, no later event wakes the work it deferred, so
// without this wake every pressure cycle ended in an idle project whose note's
// "next step" was never executed. It is forced: the follow-up round must run on
// the note alone even when the board says nothing changed.
const wakeHandoffContinue = "handoff"

// wakeRestart is the boot watchdog's automatic recovery wake: the control plane
// restarted while the project was running, the crash sweep converged the dead
// runs, and the project must continue without an operator click. It is forced
// for the same reason an operator resume is: the board may look unchanged (the
// interrupted directions returned to their old pending state), so the attention
// gate would otherwise skip the round that re-plans after the crash.
const wakeRestart = "restart"

func isForced(reason string) bool {
	return reason == "operator" || reason == "manual" || reason == "hint" || reason == "idle" ||
		reason == wakeHandoffContinue || reason == wakeRestart
}

// idleReplanSection asks for a next step or a specific blocker when a goal is
// open and prior work has settled. A handoff records that judgment in the note
// for the incoming commander to check against the current ledger.
func (r *Runner) idleReplanSection(b board.View, handoff bool) string {
	if r == nil || r.Store == nil || !r.idleGoalNeedsOperator() {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## 目标未达成，没有在办意图（本轮给出下一步，或写明卡点）\n")
	open := 0
	for _, g := range r.Store.Goals() {
		if g.Closed || g.Met {
			continue
		}
		fmt.Fprintf(&sb, "- 开放目标 %s：%s\n", g.ID, clipLine(g.Statement))
		open++
		if open >= 3 {
			break
		}
	}
	var pending []model.OperatorRequest
	for _, req := range r.Store.PendingOperatorRequests() {
		if req.Generic {
			pending = append(pending, req)
		}
	}
	if handoff {
		sb.WriteString("本轮交付交接笔记。记下拟议的下一步、具体的操作员请求或当前卡点，并写明判断依据。下一轮按当前有效指令与账本核对后再判断。\n")
	} else {
		sb.WriteString("本轮若确实无可推进，写明卡在哪、在等什么即可；也可以：\n")
		sb.WriteString("1. 派发或续跑一个在三份项目文档范围内的意图；\n")
		sb.WriteString("2. 提交一条具体的操作员请求：资产 / 动作 / 影响 / 回滚或清理 / 请求上限（有 `or_x` 用 `PUT /api/operator-requests/or_x` 补，没有用 `POST /api/operator-requests` 新建）；\n")
		sb.WriteString("3. 判定目标在三份项目文档下不可达，写清缺哪一项口径或哪类证据，请操作员改口径或关闭目标。\n")
	}
	sb.WriteString("局部止损只关闭那一条，不等于项目停工。\n")
	for _, req := range pending {
		fmt.Fprintf(&sb, "- 待补明细：`%s`（当前：%s）。用 `PUT /api/operator-requests/%s` 补齐 action/asset/impact/authorization（已有具体请求内容时）。\n", req.ID, clipLine(req.Action), req.ID)
	}
	r.mu.Lock()
	noAction := r.noActionRounds
	r.mu.Unlock()
	if noAction >= 3 && !handoff {
		fmt.Fprintf(&sb, "- 看门狗：你已经连续 %d 轮没有登记动作；若确实无可推进，写明卡在哪、在等什么。\n", noAction)
	}
	sb.WriteString("\n")
	return sb.String()
}

// supervisionIdle is how long a running intent may be silent before a
// heartbeat is worth a round even when the board digest is unchanged. A quiet
// run is not the same as a quiet board.
const supervisionIdle = 5 * time.Minute

// maxIdleReplans is the per-episode cap: one replan when the fallback appears,
// one watchdog escalation after repeated no-action rounds. It resets as soon as
// a direction exists or the goal closes.
const maxIdleReplans = 2

// stallEscalationRounds is when one dry episode earns its single alert. With the
// default 5-minute heartbeat, six decision rounds are about half an hour with no
// new fact, no new direction and no closed direction — the same window the run
// watchdog uses before it calls a silent run stuck (--idle-timeout defaults to
// 30m). Fewer rounds fire during one long tool call; more let a wedged direction
// idle for over an hour. A constant on purpose: this is an observation, and the
// operator asked for no new configuration surface.
const stallEscalationRounds = 6

type supervisionMark struct {
	failures    int
	silentSince time.Time
}

func supervisionMarks(b board.View) map[string]supervisionMark {
	marks := make(map[string]supervisionMark, len(b.Supervision))
	for _, p := range b.Supervision {
		m := supervisionMark{failures: p.ToolFailures}
		if time.Duration(p.IdleSeconds)*time.Second >= supervisionIdle {
			m.silentSince = p.StartedAt
			if p.LastEventAt != nil {
				m.silentSince = *p.LastEventAt
			}
		}
		marks[p.Run] = m
	}
	return marks
}

// A failed tool can keep emitting events forever. Supervision notices new
// structured failures as well as new silence episodes, without repeatedly
// waking for an unchanged idle run or for the commander's own bookkeeping.
func (r *Runner) heartbeatWorthwhile() bool {
	a := r.attention()
	if a.More || len(a.Reasons) > 0 {
		return true
	}
	b := board.Build(r.Store, 20)
	return r.supervisionWorthwhile(b)
}

// heartbeatIdleDetail is what the card says when the heartbeat found no round
// worth running. An open goal with no pending or running direction is not
// "nothing changed": it is a decision the operator has to make (authorize the
// next path or supply a direction), and the card must point at that instead of
// a blank idle.
func (r *Runner) heartbeatIdleDetail() string {
	if r.Store == nil {
		return "心跳：没有需要处理的变化"
	}
	open := false
	for _, g := range r.Store.Goals() {
		if !g.Closed && !g.Met {
			open = true
			break
		}
	}
	if !open {
		return "心跳：没有需要处理的变化"
	}
	// Only a direction that can still move by itself is work in flight. failed and
	// review_failed are settled (model.Settled): they need a decision, not a
	// wait, so they must not hide the idle state from the operator. Counting them
	// as in flight made the card claim "没有需要处理的变化" while the goal was open
	// and nothing was running.
	for _, in := range r.Store.ListIntents() {
		if !model.Settled(in.Status) {
			return "心跳：没有需要处理的变化"
		}
	}
	return "目标未达成，没有在办意图：等操作员下发或新意图"
}

// idleGoalNeedsOperator is the idle state that deserves a structured operator
// request: an open goal, at least one direction already attempted, and nothing
// in flight. A fresh goal with no direction yet is not this state — the
// commander still has to do the first dispatch.
func (r *Runner) idleGoalNeedsOperator() bool {
	if r.Store == nil {
		return false
	}
	open := false
	for _, g := range r.Store.Goals() {
		if !g.Closed && !g.Met {
			open = true
			break
		}
	}
	if !open {
		return false
	}
	intents := r.Store.ListIntents()
	if len(intents) == 0 {
		return false
	}
	// A settled direction is not in flight: a failed one was attempted but cannot
	// move by itself, so the idle decision still belongs to the operator. Treating
	// failed/review_failed as live work suppressed the structured idle request
	// exactly when the project was stuck.
	for _, in := range intents {
		if !model.Settled(in.Status) {
			return false
		}
	}
	return true
}

func (r *Runner) supervisionWorthwhile(b board.View) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, m := range supervisionMarks(b) {
		seen := r.lastSupervision[id]
		if m.failures > seen.failures || (!m.silentSince.IsZero() && !m.silentSince.Equal(seen.silentSince)) {
			return true
		}
	}
	return false
}

// debounce coalesces bursts, with a maximum of five quiet windows (15s by
// default). Progressive facts can arrive indefinitely; they must not starve
// the commander by resetting the quiet timer forever.
func (r *Runner) debounce(ctx context.Context, reason string) (string, bool) {
	d := r.Debounce
	if d <= 0 {
		d = 3 * time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	deadline := time.NewTimer(5 * d)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return reason, false
		case <-t.C:
			return reason, true
		case <-deadline.C:
			return reason, true
		case next := <-r.wake:
			if isForced(next) || !isForced(reason) {
				// Keep the strongest reason: forced beats normal, and a later
				// normal wake must not downgrade an already-forced round.
				reason = next
			}
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			t.Reset(d)
		}
	}
}

func (r *Runner) needsAttention() bool {
	for _, g := range r.Store.Goals() {
		if !g.Closed && (!g.Met || g.ReceiptStale) {
			return true
		}
	}
	for _, in := range r.Store.ListIntents() {
		switch in.Status {
		case model.StatusPending, model.StatusFailed, model.StatusReviewFailed:
			if !in.Hold {
				return true
			}
		case model.StatusRunning, model.StatusClaimed, model.StatusWaiting:
			return true
		case model.StatusReviewing:
			// The fixed review phase owns its own wake when it finishes. Waking
			// the commander merely because a review is running makes it poll a
			// direction it must not act on yet.
		}
	}
	// A message addressed to the commander wakes the loop directly (D88), so an
	// idle project with nothing open has nothing to attend to.
	return false
}

// workFinished is "the engagement the operator chartered is complete": at
// least one goal exists and all goals are met, with nothing pending or running.
// A project with no goals is deliberately not finished — it may be a fresh
// engagement the operator is still setting up, and auto-pausing it would fight
// the next click.
func (r *Runner) workFinished() bool {
	if r.Store == nil || r.needsAttention() || r.hasReviewing() {
		return false
	}
	for _, g := range r.Store.Goals() {
		if !g.Closed {
			return true
		}
	}
	return false
}

// hasReviewing keeps completion/pause from firing while a fixed review phase is
// still running. It does not wake the commander; the review completion does.
func (r *Runner) hasReviewing() bool {
	for _, in := range r.Store.ListIntents() {
		if in.Status == model.StatusReviewing {
			return true
		}
	}
	return false
}

// settleIfDone hands a finished project to the control plane once, so it can
// pause instead of idling. The runner cannot pause the project itself: dispatch
// gating, run cancellation and the manifest's paused flag all live in the API.
func (r *Runner) settleIfDone() {
	if r.Settled == nil || r.Paused() || !r.workFinished() {
		return
	}
	r.Settled()
}

// Run executes one decision round. It returns nil when the board digest says
// nothing changed (the cheapest possible decision).
func (r *Runner) Run(ctx context.Context, wake string, force bool) *Decision {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	// A paused decider does not spend: the operator stopped
	// it, and only an explicit Resume starts it again.
	if r.Paused() && !CloseoutRound(ctx) {
		r.mu.Lock()
		reason := r.pauseReason
		r.mu.Unlock()
		if reason == "" {
			reason = "操作员暂停了指挥官：等你点「继续」"
		}
		r.setActivity("paused", reason)
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	ctx, cancelRound := context.WithCancelCause(ctx)
	r.mu.Lock()
	if r.paused && !CloseoutRound(ctx) {
		r.mu.Unlock()
		cancelRound(ErrPaused)
		return nil
	}
	r.cancelRound = cancelRound
	r.mu.Unlock()
	defer func() {
		cancelRound(nil)
		r.mu.Lock()
		r.cancelRound = nil
		r.mu.Unlock()
	}()
	force = force || isForced(wake)
	if !force && !r.needsAttention() {
		r.setActivity("idle", "目标与意图已收口，自动休眠；新增工作或人工唤醒后再决策")
		return nil
	}
	attention := r.attention()
	// Routing a local batch does not spend a model turn. Its durable checkpoint
	// is independent of full-context delivery/ACK, so global history stays readable.
	for i := 0; len(attention.Reasons) == 0 && i < 4; i++ {
		if err := r.Store.AdvanceCommanderAttention(attention.Through); err != nil {
			r.setActivity("blocked", "保存事件路由进度失败："+err.Error())
			return nil
		}
		if !attention.More {
			break
		}
		attention = r.attention()
	}
	if !force && len(attention.Reasons) == 0 && !r.supervisionWorthwhile(board.Build(r.Store, 20)) {
		r.setActivity("idle", "局部任务自行推进，暂无需要全局判断的新事件")
		return nil
	}
	if !force && wake != "heartbeat" && len(attention.Reasons) == 1 && attention.Reasons[0].Code == "context_unacknowledged" {
		r.setActivity("idle", "局部变化已路由；未确认的全局上下文留待心跳或显式重试")
		return nil
	}
	if r.Commander == nil {
		r.setActivity("blocked", "没有可用的模型：在「设置」里配置")
		return nil
	}
	r.mu.Lock()
	if (r.paused && !CloseoutRound(ctx)) || ctx.Err() != nil {
		r.mu.Unlock()
		return nil
	}
	modelName := r.ModelName
	roundTimeout := r.RoundTimeout
	retryAfter := r.retryAfter
	r.mu.Unlock()
	if !force && r.clock().Before(retryAfter) {
		r.setActivity("blocked", "上次调用未完成，事件已保留；等待恢复重试或操作员唤醒")
		return nil
	}
	if strings.TrimSpace(modelName) == "" {
		r.setActivity("blocked", "没有可用的模型：在「设置」里配置")
		return nil
	}
	b := board.Build(r.Store, 20)
	digest := board.Digest(b)

	if r.DispatchReady != nil {
		if err := r.DispatchReady(); err != nil {
			r.setActivity("blocked", err.Error())
			return nil
		}
	}

	r.mu.Lock()
	if (r.paused && !CloseoutRound(ctx)) || ctx.Err() != nil {
		r.mu.Unlock()
		return nil
	}
	r.round++
	// Compute the supervision marks now, but commit them only when this round
	// actually handed its input to the model (see roundFinishedInput below). The
	// marks mean "the commander has been told"; committing them before the call
	// lost the wake for any round that died on its way to the model, and the
	// heartbeat could then never raise the same failure again.
	marks := supervisionMarks(b)
	r.activityWake = wake
	d := &Decision{At: time.Now(), Round: r.round, Wake: wake, Digest: digest,
		Attention: attention.Reasons, AttentionThrough: attention.Through}
	heartbeat := len(d.Attention) == 0
	if len(d.Attention) == 0 {
		code, detail := "supervision", "需要检查未被局部承接的运行状态"
		if force {
			code, detail = "explicit_wake", "操作员明确请求一次全局判断"
			switch wake {
			case wakeHandoffContinue:
				code, detail = "handoff_continue", "交接轮已写回笔记，下一轮会核对笔记与当前账本"
			case wakeRestart:
				code, detail = "restart_recovery", "控制面重启后自动恢复：核对断点与当前账本后继续"
			}
		}
		d.Attention = []store.AttentionReason{{Code: code, Kind: "control", Detail: detail}}
	}
	r.mu.Unlock()
	r.setActivity("board", "读图谱，整理本轮输入")

	stateText := ""
	var staleNoteFacts []string
	if r.State != nil {
		note, stateErr := r.State.Snapshot()
		if stateErr != nil {
			// A missing note has an explicit template. Other read failures must
			// not turn existing memory into an apparently empty fresh start.
			d.Error = "读取指挥官笔记：" + stateErr.Error()
			r.finish(d)
			r.setActivity("blocked", d.Error)
			return d
		}
		stateText = note.State
		ref := note.NoteRef
		d.NoteRead = &ref
		staleNoteFacts = r.Store.StaleNotebookFacts(note)
	}
	// This round's memory situation. The compacted/handoff halves are the
	// runner's own last-round record; resume/restart come from the caller
	// (which is the only side that can ask the executor). A nil Session is the
	// safe zero value: it injects the note in full (D89).
	rc := RoundContext{}
	if r.Session != nil {
		rc = r.Session()
	}
	if r.State != nil {
		rc.StatePath = r.State.Path()
	}
	if r.ReviewsOff != nil {
		rc.ReviewsOff = r.ReviewsOff()
	}
	rc.NoteRead = d.NoteRead
	rc.StaleNoteFacts = staleNoteFacts
	r.mu.Lock()
	if d.NoteRead != nil {
		if r.last == nil {
			rc.NoteChanged = d.NoteRead.Version > 0
		} else if r.last.NoteRead != nil {
			rc.NoteChanged = *d.NoteRead != *r.last.NoteRead && !(d.NoteRead.Writer == "commander" && d.NoteRead.RunID != "" && d.NoteRead.RunID == r.last.RunID)
		}
	}
	// Post-handoff rotation is decided once, here, from this process's memory.
	// The same answer then feeds the prompt (rc.Rotated/rc.Resume) and the
	// actual Commander call below, so the session cannot be described one way
	// and started another. pendingRotate is one-shot: consumed whether or not
	// it applies (an already-lost session has nothing to rotate), and a failed
	// new-session round is not retried on the following wake (P0-8).
	rotated := false
	if r.pendingRotate {
		r.pendingRotate = false
		if rc.Resume {
			rc.Resume = false
			rc.Rotated = true
			rotated = true
			// A new session is a new pressure cycle. If these flags survived the
			// rotation, the next climb above 85% would never schedule another
			// handoff and the note would never be rewritten again.
			r.handoffDone = false
			r.handoffForced = false
			r.handoffBypassed = false
			r.handoffMissing = 0
			r.lastContext = 0
		}
	}
	ctxStatus := ContextStatus{
		Used:            r.lastContext,
		Window:          r.windowLocked(),
		Compacted:       r.lastCompacted,
		HandoffDone:     r.handoffDone,
		HandoffForced:   r.handoffForced,
		HandoffBypassed: r.handoffBypassed,
	}
	rc.Compacted = r.lastCompacted
	// Only the round immediately after the handoff gets the whole note back.
	// The cycle flag stays true until compaction; feeding it here would paste
	// the note into every later pressure round (and call each of them "the
	// round after the handoff").
	rc.Handoff = r.handoffRound
	if rotated {
		// This round is the first round of the new session, not "the round
		// after the handoff": its rotation preamble is the memory contract.
		rc.Handoff = false
	}
	scope := r.Scope
	r.mu.Unlock()
	r.setActivity("commander", "指挥官会话运行中")
	start := time.Now()
	// The round is the unit the wake loop serves: bound it so a session that
	// waits inside the round (for executor results that can only arrive in a
	// later round) cannot hold the loop indefinitely. The cause is structured so
	// the control plane can record "cut by policy" instead of a model failure.
	roundCtx := ctx
	if roundTimeout > 0 {
		var cancel context.CancelFunc
		roundCtx, cancel = context.WithTimeoutCause(ctx, roundTimeout,
			fmt.Errorf("%w（%s）", ErrRoundTimeout, roundTimeout))
		defer cancel()
	}
	working, contextErr := retrieval.WorkingContext(r.Store, "commander")
	if contextErr != nil {
		d.Error = "读取工作上下文：" + contextErr.Error()
		r.finish(d)
		return d
	}
	if err := r.Store.BindCommanderDelivery(working.DeliveryID, attention); err != nil {
		d.Error = "绑定事件交付失败：" + err.Error()
		r.finish(d)
		return d
	}
	// Record the presentation before the model is called: the retry schedule
	// must start even when the round crashes or the model never ACKs. After
	// DeliveryPointerAfter showings the prompt degrades to the delivery id and
	// a summary, so an unacknowledging model cannot make every heartbeat carry
	// the same full page again.
	shown, showErr := r.Store.NoteDeliveryShown("commander", working.DeliveryID, r.clock())
	if showErr != nil {
		d.Error = "保存上下文交付尝试失败：" + showErr.Error()
		r.finish(d)
		return d
	}
	attentionJSON, _ := json.Marshal(d.Attention)
	lead := "## 本轮需要全局判断的原因\n" + string(attentionJSON) + "\n局部任务由其承接者继续推进；以下事件标识可跨重试保持稳定，不改变本轮指派。调用中断后先核对已登记动作与结果，未知效果不能盲目重放。\n"
	// A closeout round is the operator's explicit "judge, do not dispatch"; it
	// must not carry the idle replan obligation.
	if !CloseoutRound(ctx) {
		lead += r.idleReplanSection(b, ctxStatus.Handoff())
	}
	prompt := lead + CommanderRoundPrompt(b, stateText, scope, ctxStatus, rc) + contextDeliverySection(working, shown)
	if CloseoutRound(ctx) {
		prompt = "## 操作员请求指挥官收口\n项目保持暂停。本轮只核对当前目标与已有事实、证据，整理完成依据并通过 goals/{id}/complete 写入有证据的完成收据。不要派发新任务或新增探测；不满足目标时明确缺口，保留未达成状态，不得编造事实或把停止等同于完成。整理好笔记与操作员答复后结束本轮，等待操作员决定。\n\n" + prompt
	}
	if r.OnWake != nil {
		reasons := make([]string, 0, len(d.Attention))
		for _, a := range d.Attention {
			reasons = append(reasons, a.Code)
		}
		r.OnWake(reasons, heartbeat, len(prompt))
	}
	roundCtx = context.WithValue(roundCtx, commanderDeliveryKey{}, working.DeliveryID)
	roundCtx = context.WithValue(roundCtx, commanderInputKey{}, CommanderInput{Round: d.Round, Note: d.NoteRead})
	res, err := r.Commander(roundCtx, prompt, rc.Resume)
	d.Handoff = ctxStatus.Handoff()
	d.Compacted = res.Compacted
	d.ContextTokens, d.ContextWindow = res.ContextTokens, res.ContextWindow
	if d.ContextWindow == 0 {
		d.ContextWindow = ctxStatus.Window
	}
	d.ContextPercent = (ContextStatus{Used: d.ContextTokens, Window: d.ContextWindow}).Percent()
	d.NewFacts = len(r.Store.ListFacts("", 0)) - b.Facts.Total
	timedOut := errors.Is(context.Cause(roundCtx), ErrRoundTimeout)
	d.MS = time.Since(start).Milliseconds()
	d.Tokens = res.Tokens
	d.Actions = res.Actions
	d.RunID = res.RunID
	after := uint64(0)
	if d.NoteRead != nil {
		after = d.NoteRead.Version
	}
	handoffReceipt, validReceipt := r.Store.ValidNoteHandoff(res.RunID, after)
	d.Model = modelName
	switch {
	case errors.Is(context.Cause(roundCtx), ErrPaused):
		d.Notes = append(d.Notes, "操作员暂停：本轮已取消，不再自动调用指挥官")
	case timedOut:
		// Not a model failure: the round did its work, it just did not end.
		// The session survives the cancel, so the next wake continues it.
		d.Notes = append(d.Notes, fmt.Sprintf("本轮超过墙钟上限 %s，已截断：下一次唤醒重新决策（会话保留）", roundTimeout))
	case err != nil:
		d.Error = err.Error()
	default:
		d.Notes = append(d.Notes, "由 harness 指挥官会话执行（D78）")
	}
	if rc.Rotated {
		// Auditable, never a silent session swap: the decision log says this
		// was a planned rotation after a verified handoff, not a lost session.
		d.Notes = append(d.Notes, "交接回执有效：本轮按计划轮换到新指挥官会话（resume=false；计划内轮换，不是会话丢失；入口是笔记、board 与项目文档）")
	}
	r.mu.Lock()
	// A standby pause is requested by this very round, so the round DID finish
	// its input and its delivery checkpoint is still valid; an operator pause
	// cancels the round, so its checkpoint is not. The run-context check already
	// rejects every canceled round; the selfPaused exception keeps the standby
	// round from being replayed as unprocessed after the operator resumes.
	roundFinishedInput := err == nil && !timedOut && roundCtx.Err() == nil && (!r.paused || r.selfPaused || CloseoutRound(ctx))
	if roundFinishedInput {
		// The board this round read is now the commander's baseline: the same
		// failure or silence must not buy a second round on the next heartbeat.
		r.lastSupervision = marks
		if checkpointErr := r.Store.CompleteCommanderAttention(attention); checkpointErr != nil {
			d.Error = "模型调用已结束，但保存事件交付进度失败：" + checkpointErr.Error()
		} else {
			d.AttentionDelivered = true
		}
	}
	if !d.AttentionDelivered {
		r.retryAfter = r.clock().Add(30 * time.Second)
	} else {
		r.retryAfter = time.Time{}
	}
	r.mu.Unlock()
	r.mu.Lock()
	if res.ContextTokens > 0 {
		r.lastContext = res.ContextTokens
	}
	if res.ContextWindow > 0 {
		// The harness resolved the model's real window; stop guessing from the
		// profile default (D80).
		r.observedWindow = res.ContextWindow
	}
	// A round that never reached the model (host not ready, journal or identity
	// failure, pause) has no opinion about compaction; keeping the previous flag
	// is what makes the next real round inject the full note and the "memory is
	// lossy" warning. A successful round reporting false is still cleared.
	if err == nil && !timedOut {
		r.lastCompacted = res.Compacted
	}
	// thisHandoff is whether the round that just ended was the 85% handoff.
	// handoffDone is the pressure-cycle flag ("do not hand off again until the
	// context resets"); handoffRound is the one-round flag the next prompt reads
	// ("your note was just rewritten, here it is in full once"). A standby round
	// counts as finished too: it was not cut, only the next wake is forbidden.
	thisHandoff := ctxStatus.Handoff() && err == nil && roundCtx.Err() == nil && (!r.paused || r.selfPaused || CloseoutRound(ctx))
	handoffWrote := thisHandoff && validReceipt
	if handoffWrote {
		r.handoffMissing = 0
		r.handoffBypassed = false
		// The pressure cycle closed with a valid receipt: the next round starts
		// a fresh commander session from the note + board. Rotation is built in
		// (P0-8): it is what caps the cost of a long engagement, because the
		// 85-100% stretch of one session is by far the most expensive tokens
		// (every request pays the whole context, cached or not in the ledger's
		// accounting), and a fresh session continues from a ~8% context. Armed
		// in this process's memory only, one-shot. A forced repair round whose
		// receipt is valid closes the cycle the same way, so it arms too.
		r.pendingRotate = true
	} else if thisHandoff && !handoffWrote {
		// Only a round that really ran and ended can be "missing a receipt". A
		// gateway error, a host that was not ready or a round cut by the wall
		// clock never gave the model a chance to write; counting those released
		// the handoff pressure (and said "check the note API") for a transport
		// failure it did not have.
		r.handoffMissing++
	}
	if handoffWrote {
		d.HandoffReceipt = handoffReceipt
	}
	switch {
	case res.Compacted:
		// Compaction ends the pressure cycle. The note may have been written
		// by the earlier 85% round, before this ordinary round reached the
		// compaction threshold. Only an unhanded-off cycle needs repair.
		r.handoffDone = false
		r.handoffForced = !ctxStatus.HandoffDone && !handoffWrote
		r.handoffBypassed = false
		if !ctxStatus.Handoff() {
			r.handoffMissing = 0
		}
	case thisHandoff && handoffWrote:
		r.handoffDone = true
		r.handoffForced = false
	case thisHandoff:
		// The handoff round was supposed to rewrite the note and did not. Keep
		// the pressure armed so the next round retries instead of marking the
		// cycle complete.
		r.handoffDone = false
	}
	if window := r.windowLocked(); window > 0 {
		if pct := (ContextStatus{Used: r.lastContext, Window: window}).Percent(); pct > 0 && pct < HandoffWarnPercent {
			// A larger window or a much smaller session can leave the warning
			// band without a compaction event; the next climb is a new cycle.
			r.handoffDone = false
			if !ctxStatus.Handoff() && !r.handoffForced {
				r.handoffBypassed = false
				r.handoffMissing = 0
			}
		}
	}
	if r.handoffMissing >= MaxHandoffAttempts {
		r.handoffBypassed = true
		r.handoffForced = false
		d.Notes = append(d.Notes, "看门狗：交接连续 2 次无有效回执，已释放交接压力；未标记交接成功，请检查笔记接口与身份认证")
	}
	d.HandoffMissing, d.HandoffReleased = r.handoffMissing, r.handoffBypassed
	if !ctxStatus.Handoff() && roundFinishedInput {
		acted := d.NewFacts > 0
		for _, action := range d.Actions {
			if action.Kind != "state" {
				acted = true
			}
		}
		if acted {
			r.noActionRounds = 0
		} else {
			r.noActionRounds++
		}
		if r.noActionRounds >= 3 {
			d.Notes = append(d.Notes, fmt.Sprintf("看门狗：连续 %d 个非交接轮无登记动作或新增事实", r.noActionRounds))
		}
	}
	d.NoActionRounds = r.noActionRounds
	// The full note is re-injected only after a handoff that actually wrote it.
	r.handoffRound = handoffWrote
	handoffDone := r.handoffDone
	r.mu.Unlock()
	// Persist the pressure-cycle state on the round row. The in-memory flag dies
	// with the process, and seedContext recovers the next session's pressure from
	// the last run; without this a closed cycle was reopened by every restart and
	// the first wake spent a round on a handoff that dispatches nothing (D80).
	// A failed write does not fail the round (its work is done and delivered), but
	// it must not be silent: the row is the only record that keeps a restart from
	// reopening the cycle, so the decision carries the error and the live activity
	// stays blocked until the next write succeeds.
	var cycleStateErr error
	if roundFinishedInput && res.RunID != "" && r.Store != nil {
		if err := r.Store.RecordRunHandoffDone(res.RunID, handoffDone); err != nil {
			cycleStateErr = err
			d.Notes = append(d.Notes, "记录交接周期状态失败："+err.Error())
		}
	}
	if ctxStatus.Handoff() {
		// A handoff round deliberately dispatches nothing: counting it as "no
		// progress" would pollute the no-new-facts observation.
		note := fmt.Sprintf("上下文 %d%%：本轮是交接轮，重写 state.md 后收口（D80）", ctxStatus.Percent())
		if d.HandoffReleased {
			note += "；没有写笔记的有效本轮版本回执，交接失败上限已触发，下一轮恢复处理工作"
		} else if !handoffWrote {
			note += "；但没有写笔记的有效本轮版本回执，下一轮会重试交接"
		}
		d.Notes = append(d.Notes, note)
	} else if roundFinishedInput {
		r.observeStall(d)
	}
	// The handoff round is a step, not an ending. It deliberately dispatches
	// nothing, so the note's "next step" has no in-flight run that could wake it:
	// without this forced follow-up the project idles after every pressure cycle
	// with the deferred work unexecuted. A paused/standby round wakes nothing
	// (Wake drops while paused); the first round after a restart heals itself
	// through seedContext's persisted cycle state instead.
	if handoffWrote {
		r.Wake(wakeHandoffContinue)
	}
	r.finish(d)
	// finish() writes the idle line; a cycle-state write that failed must stay
	// visible instead, because the next restart will reopen the cycle it failed
	// to close (a handoff round that dispatches nothing).
	if cycleStateErr != nil {
		r.setActivity("blocked", "记录交接周期状态失败："+cycleStateErr.Error())
	}
	return d
}

type commanderDeliveryKey struct{}

type commanderInputKey struct{}

type CommanderInput struct {
	Round int
	Note  *model.NoteRef
}

func CommanderRoundInput(ctx context.Context) CommanderInput {
	input, _ := ctx.Value(commanderInputKey{}).(CommanderInput)
	return input
}

// CommanderDeliveryID identifies the context page prepared by the runner.
// The API binds it to the actual run before starting the executor, without
// extracting bookkeeping from prompt text.
func CommanderDeliveryID(ctx context.Context) string {
	id, _ := ctx.Value(commanderDeliveryKey{}).(string)
	return id
}

// contextDeliverySection is the "work context" half of the round prompt. A
// pending delivery is replayed verbatim until the model acknowledges it, so the
// acknowledgement requirement is restated in both forms. After
// DeliveryPointerAfter full showings the repeated page degrades to its identity
// and a change summary: the model reads the full context through
// GET /api/context and still has to ACK. Only the payload degrades, never the
// acknowledgement.
func contextDeliverySection(ctx retrieval.Context, d store.Delivery) string {
	if d.Attempts <= store.DeliveryPointerAfter {
		raw, _ := json.Marshal(ctx)
		return "\n## 可续读的工作上下文\n" + string(raw) +
			"\n这是本轮交付，不代表已经处理。考虑完这些变化与历史目录，或把待处理引用明确保存在笔记后，使用 context/ack 确认 delivery_id；未确认交付按退避重投，多次未确认后只展示摘要，原交付仍可查询。需要更多前提、历史与正文时可以继续查询检索接口。\n"
	}
	summary := struct {
		DeliveryID  string `json:"delivery_id"`
		Attempts    int    `json:"attempts"`
		Changes     int    `json:"changes"`
		SurveyFacts int    `json:"survey_facts"`
		Read        string `json:"read"`
		Acknowledge string `json:"acknowledge"`
		Note        string `json:"note"`
	}{
		DeliveryID:  ctx.DeliveryID,
		Attempts:    d.Attempts,
		Changes:     len(ctx.Changes.Changes),
		SurveyFacts: len(ctx.Survey.Facts),
		Read:        "GET /api/context",
		Acknowledge: "POST /api/context/ack",
		Note:        "同一交付已多次展示且未确认：本轮只给摘要，不重复全文；处理后仍要确认这个 delivery_id。",
	}
	raw, _ := json.Marshal(summary)
	return fmt.Sprintf("\n## 可续读的工作上下文（同一交付已展示 %d 次，本轮只给摘要）\n%s"+
		"\n这是本轮交付（摘要），不代表已经处理。需要全文时用 GET /api/context；考虑完这些变化与历史目录，或把待处理引用明确保存在笔记后，使用 context/ack 确认 delivery_id。没有确认的交付会按退避继续重投。\n",
		d.Attempts, string(raw))
}

// observeStall advances the durable no-progress counter after a decision round
// that actually reached the model and was not a handoff round. "Progress" is
// the ledger's differential, not the round's prose: a new fact or a closed
// direction (Store.ObserveDecisionRound). Opening a direction is deliberately
// not progress — it moves work around without adding evidence — but the alert
// reports how many were opened, so "splitting directions, producing no facts"
// is visible instead of a vague "no progress". The escalation is one
// operator-visible line in the decision log; it changes no dispatch, pause or
// intent state, and the store's durable flag keeps the following rounds from
// repeating it.
func (r *Runner) observeStall(d *Decision) {
	if r.Store == nil {
		return
	}
	obs, err := r.Store.ObserveDecisionRound(stallEscalationRounds)
	if err != nil {
		// Observation must not fail the round, but a failed write must not be
		// silent either: the next round would then count from a stale watermark.
		d.Notes = append(d.Notes, "记录连续无进展计数失败："+err.Error())
		return
	}
	r.mu.Lock()
	r.stalled = obs.Rounds
	r.mu.Unlock()
	if obs.Escalate {
		detail := "没有新增事实、也没有关闭意图"
		if obs.Directions > 0 {
			detail = fmt.Sprintf("期间新增 %d 条意图、零新增事实，也没有关闭意图", obs.Directions)
		}
		d.Notes = append(d.Notes, fmt.Sprintf("看门狗：意图连续 %d 轮没有进展（%s）；只提示，不自动停意图、不改变派发。", obs.Rounds, detail))
	}
}

func (r *Runner) finish(d *Decision) {
	r.mu.Lock()
	r.last = d
	r.tokens += d.Tokens
	r.mu.Unlock()
	r.appendLog(d)
	if r.Paused() {
		r.mu.Lock()
		reason := r.pauseReason
		r.mu.Unlock()
		if reason == "" {
			reason = "指挥官已暂停：等你点「继续」"
		}
		r.setActivity("paused", reason)
		return
	}
	r.setActivity("idle", idleDetail(d))
}

// idleDetail is the one line the graph's card keeps showing between rounds:
// what the last round did, or why it did nothing.
func idleDetail(d *Decision) string {
	switch {
	case d == nil:
		return "等待唤醒"
	case d.Error != "":
		return "上一轮出错：" + clipLine(d.Error)
	case len(d.Actions) == 0:
		if len(d.Notes) > 0 {
			return "上一轮没有动作：" + clipLine(d.Notes[0])
		}
		return "上一轮没有动作"
	}
	parts := make([]string, 0, 4)
	for i, a := range d.Actions {
		if i == 3 {
			parts = append(parts, "…")
			break
		}
		if a.Text != "" {
			parts = append(parts, a.Kind+" "+a.Text)
		} else {
			parts = append(parts, a.Kind)
		}
	}
	return "上一轮：" + clipLine(strings.Join(parts, " · "))
}

// clipLine keeps an activity detail to one short line: the card is a glance,
// not a second decision log.
func clipLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	r := []rune(s)
	if len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

func (r *Runner) appendLog(d *Decision) {
	if r.LogPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.LogPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(r.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, err := json.Marshal(d)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
}

// ReadLog returns the last N decisions, oldest first.
func ReadLog(path string, limit int) ([]Decision, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Decision{}, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	out := make([]Decision, 0, len(lines))
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var d Decision
		if json.Unmarshal([]byte(ln), &d) == nil {
			out = append(out, d)
		}
	}
	return out, nil
}

// hourlyRunTokens is what every run (intent runs and commander rounds) spent in
// the window. It is observation for the console, never a budget.
func hourlyRunTokens(st *store.Store, since time.Time) int {
	total := 0
	for _, run := range st.Runs() {
		if run.StartedAt.After(since) {
			total += run.Tokens
		}
	}
	return total
}

type closeoutKey struct{}

// CloseoutRound is a single operator-authorized round with dispatch still paused.
func CloseoutRound(ctx context.Context) bool {
	value, _ := ctx.Value(closeoutKey{}).(bool)
	return value
}

func (r *Runner) RunCloseout(ctx context.Context) *Decision {
	return r.Run(context.WithValue(ctx, closeoutKey{}, true), "closeout", true)
}
