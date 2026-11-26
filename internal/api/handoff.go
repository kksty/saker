package api

// Session handoff: replace the current session with a new one through the shared gateway,
// carrying a compact snapshot instead of the old conversation.
//
// The old session's transcript stays in its run log. The new session gets the
// full brief plus this snapshot (facts/evidence/attempts/children/wait/message
// cursor and a short note). HandoffVersion is an optimistic concurrency check:
// two turns cannot both replace the same direction.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

type handoffReq struct {
	// Profile is retired (the external gateway owns model routing). It is still
	// parsed so an old client gets an explicit 400.
	Profile string `json:"profile"`
	Note    string `json:"note"`
	// ExpectedVersion is the HandoffVersion the caller saw. The first handoff
	// uses 0.
	ExpectedVersion int `json:"expected_version"`
}

func (s *Server) handoffIntent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in, ok := s.Store.GetIntent(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "意图不存在")
		return
	}
	if msg, blocked := s.operatorHoldRefusal(r, id); blocked {
		writeJSON(w, http.StatusConflict, map[string]any{"error": msg, "code": "held_by_operator"})
		return
	}
	if c := s.caller(r); c.agent {
		if c.participant != collab.ParticipantID(id) {
			writeErr(w, http.StatusForbidden, "只能为自己的意图办理交接")
			return
		}
		if in.Status == model.StatusRunning && in.RunID != c.runID {
			writeErr(w, http.StatusConflict, "这个 turn 不是当前承接者")
			return
		}
	}
	var req handoffReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	updated, handoff, runID, err := s.HandoffIntent(r.Context(), id, req.Profile, req.Note, req.ExpectedVersion)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrHandoffConflict):
			// The caller's version is stale (another session handed the direction
			// over). The console refreshes and keeps the typed note; the machine
			// code is what tells it this is a version conflict, not a refusal.
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "handoff_conflict"})
			return
		case errors.Is(err, store.ErrHandoffUnarchived):
			writeErr(w, http.StatusConflict, err.Error())
			return
		case strings.Contains(err.Error(), "已结束"):
			writeErr(w, http.StatusConflict, err.Error())
			return
		default:
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"intent": updated, "handoff": handoff, "run_id": runID,
	})
}

// releaseHandoffHold undoes the hold a failed handoff took, and says so when the
// release itself fails.
//
// ReleaseHandoffHold is the only thing that clears Hold and HandoffOperation, and
// BeginHandoff refuses while HandoffOperation is set: a silent failure here leaves
// a direction that can neither be dispatched nor handed off again, with the
// caller's own error saying nothing about it.
func (s *Server) releaseHandoffHold(id, why, owner string) {
	if err := s.Store.ReleaseHandoffHold(id, why, owner); err != nil {
		log.Printf("handoff %s: 释放 Hold 失败（%s）：%v；该意图仍处于交接态，派发与再次交接都会被拒", id, why, err)
	}
}

// HandoffIntent stops the old turn (if any), writes a versioned snapshot and
// starts a new session with a fresh brief. The model connection is the
// instance-wide gateway, so the caller cannot choose a profile (toProfile is
// accepted only to reject it with one clear error).
//
// Ordering is part of the contract: the version CAS and the handoff Hold are
// taken before any side effect, so a stale or racing request never cancels the
// live run. Hold closes the window where pump could restart the direction while
// the old run is being stopped; the successful claim of the replacement run
// clears it (store.ClaimIntent), and an aborted handoff releases it explicitly.
func (s *Server) HandoffIntent(ctx context.Context, id, toProfile, note string, expectedVersion int) (model.Intent, model.Handoff, string, error) {
	if strings.TrimSpace(toProfile) != "" {
		return model.Intent{}, model.Handoff{}, "", fmt.Errorf("交接不再选择模型，请移除 profile")
	}
	in, ok := s.Store.GetIntent(id)
	if !ok {
		return model.Intent{}, model.Handoff{}, "", fmt.Errorf("意图不存在")
	}
	switch in.Status {
	case model.StatusPending, model.StatusClaimed, model.StatusRunning, model.StatusWaiting:
	default:
		return model.Intent{}, model.Handoff{}, "", fmt.Errorf("意图已结束（%s）：先 retry/reopen，再交接", in.Status)
	}
	if in.Node != "" && in.Node != s.LocalNode {
		return model.Intent{}, model.Handoff{}, "", fmt.Errorf("意图在远端节点 %s：AOP 会话属于该节点的 Host，交接只能在本机进行", in.Node)
	}
	// CAS + Hold before touching the live run: a duplicate click carrying an old
	// expected_version must fail here, with no run cancelled and no wait touched.
	held, err := s.Store.BeginHandoff(id, expectedVersion)
	if err != nil {
		return model.Intent{}, model.Handoff{}, "", err
	}
	in = held
	owner := held.HandoffOperation
	if in.Status == model.StatusRunning || in.Status == model.StatusClaimed {
		if in.RunID != "" {
			if err := s.CancelRunWithCause(in.RunID, model.CancelCauseHandoff, "新执行者接班"); err != nil {
				log.Printf("handoff %s: 停止旧 run 失败：%v", id, err)
			}
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			cur, ok := s.Store.GetIntent(id)
			if !ok {
				s.releaseHandoffHold(id, "意图在交接时消失", owner)
				return model.Intent{}, model.Handoff{}, "", fmt.Errorf("意图在交接时消失")
			}
			if cur.Status != model.StatusRunning && cur.Status != model.StatusClaimed {
				in = cur
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if in.Status == model.StatusRunning || in.Status == model.StatusClaimed {
			s.releaseHandoffHold(id, "旧 run 未停下", owner)
			return model.Intent{}, model.Handoff{}, "", fmt.Errorf("旧 run 还没停下，稍后重试交接")
		}
	}
	snapshot := s.buildHandoff(in, note)
	saved, err := s.Store.SaveHandoff(id, expectedVersion, snapshot, owner)
	if err != nil {
		s.releaseHandoffHold(id, "交接版本冲突", owner)
		return model.Intent{}, model.Handoff{}, "", err
	}
	snapshot = *saved.Handoff
	runID, err := s.startRunOwned(id, "", "", false, false, owner)
	if err != nil {
		// The snapshot is durable; release the hold so the direction stays a
		// normal pending unit that can be run/retried (existing semantics).
		s.releaseHandoffHold(id, "新执行者启动失败", owner)
		return saved, snapshot, "", err
	}
	_, _ = s.Store.UpdateRun(runID, func(r *model.Run) {
		r.HandoffFrom = snapshot.FromRun
		r.HandoffVersion = saved.HandoffVersion
	})
	run, _ := s.Store.GetRun(runID)
	snapshot.ToGeneration = run.Generation
	// The child results that finished during the handoff window are now the new
	// run's to consume; let the scheduler settle/queue whatever changed.
	go s.pump()
	return saved, snapshot, runID, nil
}

func (s *Server) buildHandoff(in model.Intent, note string) model.Handoff {
	db := s.Store.Snapshot()
	h := model.Handoff{
		// FromProfile is read-only history from the retired profile layer; the
		// replacement session uses the external gateway like every other run.
		FromRun: in.RunID, FromProfile: in.Profile, FromGeneration: in.Attempts,
		ToGeneration: in.Attempts + 1,
		Note:         strings.TrimSpace(note),
	}
	if in.Wait != nil {
		w := *in.Wait
		w.Actions = append([]string(nil), in.Wait.Actions...)
		w.Results = append([]model.WaitResult(nil), in.Wait.Results...)
		h.Wait = &w
	}
	seen := map[string]bool{}
	evidence := map[string]string{}
	for _, ev := range db.Evidence {
		evidence[ev.ID] = ev.URI
	}
	for _, f := range db.Facts {
		if f.IntentID != in.ID || f.Unusable() {
			continue
		}
		if f.Status != "verified" && len(f.EvidenceIDs) == 0 {
			continue
		}
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		h.Facts = append(h.Facts, f.ID)
		for _, eid := range f.EvidenceIDs {
			if uri := evidence[eid]; uri != "" && !contains(h.Evidence, uri) {
				h.Evidence = append(h.Evidence, uri)
			}
		}
		if len(h.Facts) >= 30 {
			break
		}
	}
	// Newest first, and only runs that actually said something.
	runs := s.Store.Runs()
	for i := len(runs) - 1; i >= 0 && len(h.Attempts) < 5; i-- {
		r := runs[i]
		if r.IntentID != in.ID || r.CancelCause == model.CancelCauseHandoff {
			continue
		}
		reason := strings.TrimSpace(r.StopReason)
		if reason == "" {
			continue
		}
		h.Attempts = append(h.Attempts, fmt.Sprintf("%s/%s: %s", r.Status, r.Model, clipRunes(reason, 200)))
	}
	if msgs := s.Store.Messages(1); len(msgs) > 0 {
		h.MessageCursor = msgs[len(msgs)-1].Seq
	}
	return h
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// handoffAuthorization is the authorization text a replacement session must
// read verbatim.
//
// It is rendered by the dispatcher, not rebuilt here: a handoff section that
// derived the boundary from the handoff note would hand the next model the
// previous model's paraphrase, and nothing downstream could tell the two apart.
// The text therefore arrives exactly as the task book already carries it, and
// SourceBrief points at the archived dispatch of the run being replaced, so a
// reader can recover the original bytes instead of trusting this copy.
type handoffAuthorization struct {
	// Briefing is the authorization boundary plus project description, as the
	// task book renders it.
	Briefing string
	// BriefRevision is when the operator last rewrote the brief; zero means the
	// project predates the field (the caller falls back to creation time).
	BriefRevision time.Time
	// SourceRun is the run being replaced (model.Handoff.FromRun).
	SourceRun string
	// SourceBrief is that run's archived dispatch text, empty when it has none.
	SourceBrief string
	// SourceArchiveStatus/Error say whether the path above is trustworthy. A
	// half-written archive (file exists, ledger path did not commit) must not be
	// presented as readable just because a path string was found.
	SourceArchiveStatus string
	SourceArchiveError  string
}

func handoffSection(db store.DB, in model.Intent, h model.Handoff, auth handoffAuthorization, showUnverified bool) string {
	byID := map[string]model.Fact{}
	for _, f := range db.Facts {
		byID[f.ID] = f
	}
	var b strings.Builder
	b.WriteString("\n\n## 交接（新执行者）\n")
	b.WriteString("旧对话没有带过来。以下是交接索引，完整事实与历史可按 ID 续读；本轮前提和约束以当前任务书为准。")
	b.WriteString("历史判断描述当时条件；结合当前观察自主决定后续行动。\n")
	// The operator's own text comes before the previous model's account: a
	// replacement that reads the handoff first has already formed its picture by
	// the time it reaches the operator's words. The text is rendered verbatim;
	// no heading explains what it is or how it outranks the handoff note.
	writeScope(&b, auth.Briefing)
	revision := "未知（升级前写入的项目没有修订时间）"
	if !auth.BriefRevision.IsZero() {
		revision = auth.BriefRevision.Format(time.RFC3339)
	}
	b.WriteString("简报修订时间：" + revision + "。\n")
	b.WriteString("\n### 原文出处\n")
	if strings.TrimSpace(auth.SourceBrief) != "" {
		fmt.Fprintf(&b, "- 被替换的 run：%s；它的下发全文归档：`%s`（按路径可回看当时逐字原文）", auth.SourceRun, auth.SourceBrief)
		switch auth.SourceArchiveStatus {
		case model.BriefArchiveArchived:
			b.WriteString("；归档状态：已归档\n")
		case model.BriefArchivePending:
			b.WriteString("；归档状态：未确认——文件可能已写，但账本路径未写回，不能当作可按引用读取\n")
		case model.BriefArchiveWriteFailed:
			fmt.Fprintf(&b, "；归档状态：写入失败（%s），这份原文无法回看\n", auth.SourceArchiveError)
		default:
			b.WriteString("；归档状态：旧记录未记录状态\n")
		}
	} else {
		switch auth.SourceArchiveStatus {
		case model.BriefArchiveWriteFailed:
			fmt.Fprintf(&b, "- 被替换的 run：%s；它没有下发归档：写入失败（%s），上一任读到的原文无法回看\n", auth.SourceRun, auth.SourceArchiveError)
		case model.BriefArchivePending:
			fmt.Fprintf(&b, "- 被替换的 run：%s；它没有可引用的下发归档：文件可能已写但账本路径未写回，上一任读到的原文无法按引用回看\n", auth.SourceRun)
		default:
			fmt.Fprintf(&b, "- 被替换的 run：%s；它没有下发归档，本节只能代表当前任务书的原文，上一任读到的那一份无法回看\n", auth.SourceRun)
		}
	}
	if h.Note != "" {
		b.WriteString("\n### 交接说明\n" + h.Note + "\n")
	}
	if len(h.Facts) > 0 {
		b.WriteString("\n### 已确认/有证据的事实\n")
		for _, id := range h.Facts {
			f, ok := byID[id]
			if !ok {
				fmt.Fprintf(&b, "- `%s`：当前快照不存在，不能作为前提\n", id)
				continue
			}
			if version, known := h.FactClaimVersions[id]; known && (version != f.ClaimVersion || f.Unusable()) {
				fmt.Fprintf(&b, "- `%s`：交接时记录 v%d / 断言 v%d，当前断言 v%d（%s）；引用已过期，按 ID 重读后重新判断\n", id, h.FactVersions[id], version, f.ClaimVersion, f.Status)
				continue
			}
			claim, claimKnown := h.FactClaimVersions[id]
			record, recordKnown := h.FactVersions[id]
			switch {
			case !claimKnown:
				fmt.Fprintf(&b, "- `%s`：交接时未记录断言版本；当前断言 v%d；按当前账本重读后判断：\n", id, f.ClaimVersion)
			case recordKnown:
				fmt.Fprintf(&b, "- `%s`：交接时记录 v%d / 断言 v%d；按当前账本状态展示：\n", id, record, claim)
			default:
				fmt.Fprintf(&b, "- `%s`：断言 v%d；按当前账本状态展示：\n", id, claim)
			}
			// Same display rule as an ordinary dispatch: the id, kind, status and
			// evidence index are fixed, and the body follows the project's review
			// configuration and the current claim version. A handoff snapshot must
			// not expose more of an unverified assertion than a normal brief does.
			b.WriteString(renderFact(f, showUnverified))
		}
	}
	if len(h.Evidence) > 0 {
		b.WriteString("\n### 交接时记录的证据位置（相对当前 case）\n")
		for _, uri := range h.Evidence {
			rel, err := filepath.Rel(in.ID, filepath.FromSlash(uri))
			if err == nil {
				fmt.Fprintf(&b, "- `%s`\n", filepath.ToSlash(rel))
			}
		}
	}
	if len(h.Attempts) > 0 {
		b.WriteString("\n### 此前的尝试与停止原因\n")
		for _, a := range h.Attempts {
			fmt.Fprintf(&b, "- %s\n", a)
		}
	}
	if h.Wait != nil && !h.Wait.WakePending {
		if h.Wait.Operator != nil {
			fmt.Fprintf(&b, "\n### 人工交接\n请求 %s：%s。人工是否完成以控制面收据为准，不能自行确认。\n", h.Wait.Operator.ID, h.Wait.Operator.Request)
			if h.Wait.Operator.Resolution == model.OperatorUnavailable {
				fmt.Fprintf(&b, "操作员明确无法完成：%s。缺失登录前提未获得，相关测试未覆盖，按授权与当前实际访问能力重新判断。\n", h.Wait.Operator.Response)
			} else if h.Wait.Operator.CompletedAt != nil {
				fmt.Fprintf(&b, "操作员完成说明：%s。完成回执不等于登录有效；继续前核对实际会话与访问权限。\n", h.Wait.Operator.Response)
			}

		} else if len(h.Wait.Actions) > 0 {
			fmt.Fprintf(&b, "\n### 外部动作等待\n仍在等待：%s（mode=%s，deadline=%s）。结果未知时需要核对，记录不代表执行或成功。\n", strings.Join(h.Wait.Actions, ", "), h.Wait.Mode, deadlineText(h.Wait.Deadline))
		}
	}
	fmt.Fprintf(&b, "\n消息游标：%d（用 GET /messages?after=%d 看新消息）。\n", h.MessageCursor, h.MessageCursor)
	return b.String()
}
