// Package metrics is the Phase 1 observation layer.
//
// It is deliberately a sidecar: records are appended to
// <project data>/metrics/phase1.jsonl and nothing here touches the ledger, the
// change stream, or any run payload. A missing or unwritable sidecar must never
// change behaviour, so every method is nil-safe and failures are dropped.
//
// The fields answer four questions the ledger cannot answer today:
//   - which request was a session's first, a run's first, the first after a
//     compaction, or a warm resume (cache behaviour is a property of those
//     boundaries, not of the run aggregate);
//   - how big each request was and how much of it the provider reported as a
//     cache read;
//   - why a commander round woke (the heartbeat vs event histogram);
//   - why a run was canceled, in the Phase 1 vocabulary, with the raw cause and
//     stop reason kept verbatim.
package metrics

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kksty/saker/internal/executor"
)

// FileName is the one file per project. It is append-only and never rotated by
// this package: Phase 1 reads it, it does not manage retention.
const FileName = "phase1.jsonl"

// Class names the boundary a request sits on.
const (
	ClassSessionFirst   = "session_first"
	ClassPostCompaction = "post_compaction_first"
	ClassWarmResume     = "warm_resume"
	ClassWarm           = "warm"
)

// Cancel classes are the Phase 1 vocabulary. The raw cause/reason are kept on
// the same record, so a mapping mistake never destroys the original.
const (
	CancelShutdownRestart = "shutdown_restart"
	CancelOperatorPause   = "operator_pause"
	CancelRoundTimeout    = "round_timeout"
	CancelModelStall      = "model_stall"
	CancelIdleTimeout     = "idle_timeout"
	CancelProjectStop     = "project_stop"
	CancelOther           = "other"
)

type runState struct {
	firstSeen bool
	lastCtx   int
	compacted bool // a compaction state arrived; the next request is the boundary
}

// Recorder appends Phase 1 records for one project. A nil *Recorder is valid
// and does nothing, which keeps every call site free of nil checks.
type Recorder struct {
	mu       sync.Mutex
	path     string
	sessions map[string]bool
	runs     map[string]*runState
	broken   bool
}

// Open creates the recorder for one project directory. An empty dir or an
// unwritable metrics directory returns nil: observation must not fail a run.
func Open(dataDir string) *Recorder {
	if dataDir == "" {
		return nil
	}
	dir := filepath.Join(dataDir, "metrics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	return &Recorder{path: filepath.Join(dir, FileName), sessions: map[string]bool{}, runs: map[string]*runState{}}
}

// SeedSessions marks harness sessions that already existed before this process
// opened the recorder. A restart recreates the in-memory session map, and
// without this the first request of every warm session would be labelled
// session_first instead of warm_resume — exactly the boundary Phase 1 exists to
// measure. Session ids are read from the durable run rows, never guessed.
func (r *Recorder) SeedSessions(ids ...string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			r.sessions[id] = true
		}
	}
}

// Path is the sidecar file, or "" for a nil recorder. The API surfaces it so an
// operator can find the raw records without guessing.
func (r *Recorder) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// Close exists so callers have one lifecycle hook; with an open-per-record
// writer there is no handle to release, and a test temp dir is never held open.
func (r *Recorder) Close() {}

func (r *Recorder) append(v map[string]any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// broken is guarded by mu: Observe/Cancel/Wake can call append concurrently,
	// and reading the flag before the lock is a data race.
	if r.broken {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	// Open-per-record keeps no handle between events: on Windows an open
	// sidecar makes a temp data dir impossible to remove, and the volume is a
	// few thousand rows per project, not a hot path.
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		r.broken = true
		return
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		r.broken = true
	}
	_ = f.Close()
}

// Observe consumes one harness event. Only usage, status (compaction) and tool
// result events carry Phase 1 facts; everything else is ignored.
func (r *Recorder) Observe(runID, purpose, intentID string, ev executor.Event) {
	if r == nil || runID == "" {
		return
	}
	switch ev.Kind {
	case executor.KindStatus:
		if executor.IsCompactionState(ev.State) {
			r.mu.Lock()
			st := r.state(runID)
			st.compacted = true
			r.mu.Unlock()
		}
	case executor.KindToolResult:
		if ev.Text != "" {
			r.append(map[string]any{
				"t": "tool", "at": time.Now().UnixMilli(), "run": runID,
				"bytes": len(ev.Text), "ok": ev.IsOK(), "tool": ev.Tool,
			})
		}
	case executor.KindUsage:
		r.usage(runID, purpose, intentID, ev)
	}
}

func (r *Recorder) state(runID string) *runState {
	st := r.runs[runID]
	if st == nil {
		st = &runState{}
		r.runs[runID] = st
	}
	return st
}

func (r *Recorder) usage(runID, purpose, intentID string, ev executor.Event) {
	r.mu.Lock()
	st := r.state(runID)
	runFirst := !st.firstSeen
	sessionFirst := ev.SessionID != "" && !r.sessions[ev.SessionID]
	postCompaction := st.compacted
	class := ClassWarm
	switch {
	case sessionFirst:
		class = ClassSessionFirst
	case postCompaction:
		class = ClassPostCompaction
	case runFirst:
		class = ClassWarmResume
	}
	st.firstSeen = true
	st.compacted = false
	st.lastCtx = ev.ContextTokens
	if ev.SessionID != "" {
		r.sessions[ev.SessionID] = true
	}
	r.mu.Unlock()

	r.append(map[string]any{
		"t": "request", "at": time.Now().UnixMilli(),
		"run": runID, "purpose": purpose, "intent": intentID,
		"session": ev.SessionID, "class": class,
		"run_first": runFirst, "session_first": sessionFirst, "post_compaction": postCompaction,
		"input": ev.InputTokens, "output": ev.OutputTokens,
		"cached": ev.CachedInputTokens, "cache_write": ev.CacheWriteTokens,
		"context": ev.ContextTokens, "window": ev.ContextWindow,
		"requests": ev.UsageRequests, "total": ev.Tokens,
	})
}

// Wake records one commander round's reasons. The caller passes the attention
// codes and whether the round was a plain heartbeat (no event behind it).
func (r *Recorder) Wake(reasons []string, heartbeat bool, promptBytes int) {
	r.append(map[string]any{
		"t": "wake", "at": time.Now().UnixMilli(),
		"reasons": reasons, "heartbeat": heartbeat, "bytes": promptBytes,
	})
}

// Cancel records the structured cause plus the Phase 1 classification. The raw
// strings stay on the record: a mapping bug must be visible, not silent.
func (r *Recorder) Cancel(runID, cause, reason, stopReason string) {
	r.append(map[string]any{
		"t": "cancel", "at": time.Now().UnixMilli(), "run": runID,
		"cause": cause, "reason": reason, "stop": stopReason,
		"class": ClassifyCancel(cause, reason),
	})
}

// ClassifyCancel maps the existing structured causes onto the Phase 1
// vocabulary. Unknown causes stay "other" and keep their raw string.
func ClassifyCancel(cause, reason string) string {
	switch cause {
	case "restart":
		return CancelShutdownRestart
	case "round_timeout":
		return CancelRoundTimeout
	case "model_stall":
		return CancelModelStall
	case "pause", "review_pause", "stop_loss", "project_stop":
		return CancelProjectStop
	case "operator", "operator_wait":
		return CancelOperatorPause
	case "watchdog":
		if containsAny(reason, "idle", "静默", "无事件", "no event") {
			return CancelIdleTimeout
		}
		return CancelOther
	default:
		return CancelOther
	}
}

func containsAny(s string, subs ...string) bool {
	low := strings.ToLower(s)
	for _, sub := range subs {
		if sub != "" && strings.Contains(low, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

// Summary is the read-only projection the API returns. Percentiles are over the
// recorded request rows; "commander" and "all" are reported separately because
// the project baseline is quoted in those two scopes.
type Summary struct {
	File          string         `json:"file"`
	Requests      int            `json:"requests"`
	ByClass       map[string]int `json:"by_class"`
	ByPurpose     map[string]int `json:"by_purpose"`
	CacheReadSum  int64          `json:"cache_read_sum"`
	ContextP50    int            `json:"context_p50"`
	ContextP90    int            `json:"context_p90"`
	FirstCacheP50 int            `json:"first_cache_p50"`
	FirstCacheP90 int            `json:"first_cache_p90"`
	Wakes         int            `json:"wakes"`
	WakeReasons   map[string]int `json:"wake_reasons"`
	Heartbeats    int            `json:"heartbeats"`
	Cancels       map[string]int `json:"cancels"`
}

// Summary scans the sidecar. It is intentionally O(file) and only used by the
// operator-facing endpoint, never on a dispatch path.
func (r *Recorder) Summary() Summary {
	out := Summary{ByClass: map[string]int{}, ByPurpose: map[string]int{}, WakeReasons: map[string]int{}, Cancels: map[string]int{}}
	if r == nil {
		return out
	}
	out.File = r.path
	f, err := os.Open(r.path)
	if err != nil {
		return out
	}
	defer f.Close()
	var ctxs []int
	var firsts []int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var row map[string]any
		if json.Unmarshal(sc.Bytes(), &row) != nil {
			continue
		}
		switch row["t"] {
		case "request":
			out.Requests++
			if c, ok := row["class"].(string); ok {
				out.ByClass[c]++
			}
			if p, ok := row["purpose"].(string); ok && p != "" {
				out.ByPurpose[p]++
			} else {
				out.ByPurpose["executor"]++
			}
			ctxs = append(ctxs, intOf(row["context"]))
			out.CacheReadSum += int64(intOf(row["cached"]))
			if row["run_first"] == true {
				firsts = append(firsts, intOf(row["cached"]))
			}
		case "wake":
			out.Wakes++
			if hb, _ := row["heartbeat"].(bool); hb {
				out.Heartbeats++
			}
			if rs, ok := row["reasons"].([]any); ok {
				for _, v := range rs {
					if s, ok := v.(string); ok {
						out.WakeReasons[s]++
					}
				}
			}
		case "cancel":
			if c, ok := row["class"].(string); ok {
				out.Cancels[c]++
			}
		}
	}
	out.ContextP50, out.ContextP90 = percentile(ctxs, 50), percentile(ctxs, 90)
	out.FirstCacheP50, out.FirstCacheP90 = percentile(firsts, 50), percentile(firsts, 90)
	return out
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func percentile(xs []int, p int) int {
	if len(xs) == 0 {
		return 0
	}
	sort.Ints(xs)
	idx := (len(xs) - 1) * p / 100
	return xs[idx]
}

// String is a small debugging aid used in tests.
func (s Summary) String() string {
	return fmt.Sprintf("requests=%d wakes=%d cancels=%v", s.Requests, s.Wakes, s.Cancels)
}
