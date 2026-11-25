package api

import (
	"errors"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kksty/tianwu/internal/aop"
	"github.com/kksty/tianwu/internal/aopexec"
	"github.com/kksty/tianwu/internal/brake"
	"github.com/kksty/tianwu/internal/executor"
	"github.com/kksty/tianwu/internal/model"
)

// A direction that dies on the transport — the gateway dropped the stream, the
// host closed the socket, the dial timed out — has not produced a judgement. It
// is the same work, not a new direction, so it continues on its own: same
// intent, same case, a new run, bounded and spaced out.
//
// Everything else is left alone. A credential failure is a real stop (the
// project pauses), an explicit hold/park/pause is a decision, and a direction
// that ran to completion and found nothing is the parent's call — retrying it
// with the same brief would only buy the same answer. classifyProviderFailure
// is deliberately NOT the gate here: it returns "transient" for anything it does
// not recognise, which includes tool and judgement failures. The gate is the
// transport vocabulary below, which only a broken link produces.
const maxAutoResumes = 3

// autoResumeDelays is the whole backoff schedule: 10s → 30s → 90s, and the
// last entry caps it.
var autoResumeDelays = []time.Duration{10 * time.Second, 30 * time.Second, 90 * time.Second}

// autoResumeBrake bounds recovery on the *shared* dependency, not on the
// direction. The per-direction budget above counts attempts per intent; with N
// directions behind one dead gateway, every direction can spend its own three
// chances and the fleet sends N×3 dials at a link that is already down. Nobody
// exceeded a limit and the platform still hammered itself.
//
// 30s between recoveries and at most 12 in ten minutes per gateway/node pair is
// deliberately conservative: the cost of being slow is a direction that resumes
// a little later, and the cost of being fast is the storm this exists to stop.
// A refusal is not a failure of the work: like an exhausted per-direction
// budget, it hands the direction back to the commander.
var autoResumeBrake = brake.New(brake.Options{
	Cooldown: 30 * time.Second,
	Window:   10 * time.Minute,
	Max:      12,
})

// recoveryKey names the shared dependency behind one run's transport death. The
// node is part of the key: a dead scan node fails every direction on it, while a
// healthy node sharing the same gateway must not inherit that node's cooldown.
func (s *Server) recoveryKey(runID string) string {
	provider, modelName := "", ""
	if s.ModelSettings != nil {
		provider, modelName, _ = s.ModelSettings()
	}
	node := ""
	if run, ok := s.Store.GetRun(runID); ok {
		node = run.Node
	}
	return "gateway=" + provider + "/" + modelName + ";node=" + node
}

// transportFailurePatterns is the provider-side vocabulary, used only when the
// run carries no recorded cause: the harness reports a dead dial or a broken
// stream as ordinary run output, never as a link class. Generic timeout words
// ("i/o timeout", "context deadline exceeded") are deliberately absent — a slow
// model or a slow tool produces them too, and retrying those buys the same
// answer.
var transportFailurePatterns = []string{
	"connection reset",
	"connection refused",
	"forcibly closed",
	"broken pipe",
	"dial tcp",
	"no such host",
	"tls handshake",
	"http2: ",
	"unmarshal stream chunk",
	"stream error",
	"transport is closing",
}

// transportFailure reports whether a run with no recorded cause failed at the
// link layer, read from the provider text the harness itself printed.
func transportFailure(res executor.Result) bool {
	text := strings.ToLower(res.Output + "\n" + res.StopReason)
	if strings.TrimSpace(text) == "" {
		return false
	}
	for _, needle := range transportFailurePatterns {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// transportDeath answers "did this run die on the link?" from the recorded death
// rather than from words in the output. The executor's class is authoritative
// when present: a dropped AOP stream is exactly the case the old text scan
// missed (it never reached Result.Output). Only runs with no class get the
// narrow provider-text look, because a provider dial error arrives as ordinary
// output. Journal error lines are never consulted: they include non-fatal tool
// events, and a media download timeout must not turn an unrelated failure into
// a suspected broken link.
func (s *Server) transportDeath(runID string, res executor.Result) bool {
	switch res.FailureClass {
	case aop.FailureTransport, aop.FailureUnknown:
		return true
	case aop.FailureIntentional:
		return false
	}
	if run, ok := s.Store.GetRun(runID); ok {
		switch run.FailureClass {
		case aop.FailureTransport, aop.FailureUnknown:
			return true
		case aop.FailureIntentional:
			return false
		}
	}
	return transportFailure(res)
}

// autoResumeState keeps the per-direction budget. It is process memory on
// purpose: the budget bounds a retry storm inside one process lifetime, and a
// restart is already visible as a cancelled run in the ledger. Nothing here is
// a decision about the work.
var autoResumeState = struct {
	sync.Mutex
	used map[string]int
}{used: map[string]int{}}

func autoResumeUsed(intentID string) int {
	autoResumeState.Lock()
	defer autoResumeState.Unlock()
	return autoResumeState.used[intentID]
}

func autoResumeMark(intentID string) int {
	autoResumeState.Lock()
	defer autoResumeState.Unlock()
	autoResumeState.used[intentID]++
	return autoResumeState.used[intentID]
}

// autoResumeRefund returns a budget slot that was reserved but never used to
// start a run: a timer that lost the direction to a concurrent retry/continue,
// or found a child still running, must not count as one of three attempts.
func autoResumeRefund(intentID string) {
	autoResumeState.Lock()
	defer autoResumeState.Unlock()
	if autoResumeState.used[intentID] > 0 {
		autoResumeState.used[intentID]--
	}
}

// resetAutoResumeBudget forgets the history of a direction that produced a
// result: a later failure is a new episode, not this one continuing.
func resetAutoResumeBudget(intentID string) {
	autoResumeState.Lock()
	defer autoResumeState.Unlock()
	delete(autoResumeState.used, intentID)
}

// autoResumeAfterFailure continues a direction whose run died on the transport.
// It is called from finish, after settlement, so the intent is already in its
// terminal state for this attempt; the restart goes through the same explicit
// retry entry the operator uses (CAS to pending, hold, message redelivery), not
// through a fresh dispatch the pump could race with.
func (s *Server) autoResumeAfterFailure(runID, intentID, status string, res executor.Result) {
	if status != model.RunFailed {
		return
	}
	if s.paused.Load() || s.closing.Load() || s.deleting.Load() {
		return
	}
	if !s.transportDeath(runID, res) {
		return
	}
	in, ok := s.Store.GetIntent(intentID)
	if !ok || in.Status != model.StatusFailed || in.Aborted || in.Hold {
		return
	}
	if in.RunID != "" && in.RunID != runID {
		if live, ok := s.Store.GetRun(in.RunID); ok && live.Status == model.RunRunning {
			return
		}
	}
	if used := autoResumeUsed(intentID); used >= maxAutoResumes {
		s.noteAutoResume(runID, intentID, used+1, "超过自动续跑上限，留给指挥官决定")
		return
	}
	// A direction whose children are still running must not be restarted.
	// RunFailed drops the parent's wait, so the children's results already have
	// nowhere to land (SatisfyWait needs the wait the failure cleared); restarting
	// the parent would put it on the same ground as its children, which is the
	// concurrent pair this design refuses.
	key := s.recoveryKey(runID)
	slot, reason := autoResumeBrake.Reserve(key)
	if slot == nil {
		s.noteAutoResume(runID, intentID, autoResumeUsed(intentID)+1,
			"共享依赖闸：本窗口内同一网关/节点的自动恢复次数已用尽（"+string(reason)+"），留给指挥官决定")
		log.Printf("run %s：传输失败，但共享依赖闸 %s 在本窗口内已满，不自动续跑 %s", runID, key, intentID)
		return
	}
	attempt := autoResumeUsed(intentID) + 1
	delay := autoResumeDelays[len(autoResumeDelays)-1]
	if attempt-1 < len(autoResumeDelays) {
		delay = autoResumeDelays[attempt-1]
	}
	// The dependency's own spacing can be wider than the per-direction backoff:
	// a queued recovery waits its turn instead of arriving with the herd.
	if wait := time.Until(slot.At()); wait > delay {
		delay = wait
	}
	s.noteAutoResume(runID, intentID, attempt, "")
	log.Printf("run %s：传输失败，%s 后自动续跑 %s（第 %d 次，上限 %d；依赖 %s）", runID, delay, intentID, attempt, maxAutoResumes, key)
	time.AfterFunc(delay, func() { s.autoResumeFire(runID, intentID, slot) })
}

// autoResumeFire is the timer body, kept separate so a test can drive it
// without waiting out the backoff. It carries the reserved slot: a timer that
// never starts a run gives the shared budget back, because a lost race is not a
// storm.
func (s *Server) autoResumeFire(runID, intentID string, slot *brake.Slot) {
	started := false
	defer func() {
		if !started {
			slot.Release()
		}
	}()
	if s.paused.Load() || s.closing.Load() || s.deleting.Load() {
		return
	}
	cur, ok := s.Store.GetIntent(intentID)
	if !ok || cur.Aborted || cur.Hold || cur.Status != model.StatusFailed {
		return
	}
	// The budget is spent when a retry actually starts, not when the timer is
	// armed: a timer that loses the direction to a concurrent retry, or finds a
	// child still running, must not use up one of three chances.
	attempt := autoResumeMark(intentID)
	if attempt > maxAutoResumes {
		autoResumeRefund(intentID)
		s.noteAutoResume(runID, intentID, attempt, "超过自动续跑上限，留给指挥官决定")
		return
	}
	resume := aopexec.CanResume(filepath.Join(s.CaseRoot, intentID))
	note := "自动续跑（第 " + strconv.Itoa(attempt) + " 次）：上一轮是传输失败（连接中断/宿主关闭），不是判断结论"
	_, _, err := s.retryIntentRun(intentID, note, resume)
	if err != nil {
		var re *retryError
		if errors.As(err, &re) {
			switch re.code {
			case "retry_conflict":
				// Another retry/continue took the direction between the timer
				// firing and the CAS. The work is running under a newer request;
				// this attempt never happened, so it is neither a failure to
				// record nor a chance spent.
				autoResumeRefund(intentID)
				return
			case "resume_unavailable":
				// A session that cannot be resumed is not a reason to leave the
				// work dead: fall back to a fresh session with the handoff note,
				// once.
				_, _, ferr := s.retryIntentRun(intentID, note+"；原会话不可续，改新会话继续", false)
				if ferr != nil {
					// The fallback never started a run either, so it does not
					// spend one of three chances.
					autoResumeRefund(intentID)
					log.Printf("自动续跑 %s 失败：%v / 新会话：%v", intentID, err, ferr)
					s.noteAutoResume(runID, intentID, attempt, "启动失败："+ferr.Error())
				} else {
					started = true
				}
				return
			}
		}
		// Capacity, an unusable premise or a lost claim: the run never started, so
		// this was not an attempt. Charging it here is what let a full instance
		// burn all three chances in a few minutes and leave the direction with no
		// way to heal itself.
		autoResumeRefund(intentID)
		log.Printf("自动续跑 %s 失败：%v", intentID, err)
		s.noteAutoResume(runID, intentID, attempt, "启动失败："+err.Error())
		return
	}
	started = true
}

// noteAutoResume writes the decision to the failed run's journal: the run is
// over, and "why did the same direction run again" must be answerable without
// reading server logs.
func (s *Server) noteAutoResume(runID, intentID string, attempt int, skip string) {
	line := map[string]any{
		"kind": "run_autoresume", "at": time.Now(), "run_id": runID, "intent_id": intentID,
		"attempt": attempt, "max": maxAutoResumes,
	}
	if skip != "" {
		line["skipped"] = skip
	}
	if err := s.appendJournalOnDisk(runID, line); err != nil {
		log.Printf("run %s: 记录自动续跑失败：%v", runID, err)
	}
	// The row carries the attempt count too: the console shows "the link
	// dropped, rerun #N" from the run, not from the journal.
	if skip == "" && attempt > 0 {
		if _, err := s.Store.UpdateRun(runID, func(r *model.Run) { r.AutoResumeAttempt = attempt }); err != nil {
			log.Printf("run %s: 记录自动续跑次数失败：%v", runID, err)
		}
	}
}
