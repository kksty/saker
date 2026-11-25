package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/model"
)

// settleable reports the states a declared completion may start from. A live
// direction is excluded on purpose: its run still owns the result, and writing
// done under it would be overwritten when the run finishes.
func settleable(in model.Intent) bool {
	switch in.Status {
	case model.StatusFailed, model.StatusReviewFailed, model.StatusParked:
		return true
	case model.StatusPending:
		// A stopped direction waits in pending+Hold; the commander may decide
		// the work visible in the record is enough. An idle pending row is not
		// a completion candidate: nobody produced anything for it yet.
		return in.Hold
	default:
		return false
	}
}

// settleIntent lets the commander close a direction that no run closed: the
// attempt failed, the review failed, the direction was parked, or it was
// stopped before it could report. The completion is declared, not produced —
// DoneDeclaredBy keeps that provenance on the row, and the note plus the cited
// evidence are what make the judgement auditable later.
//
// Only the operator and the harness commander reach this route; executors have
// no write route on the project mux. The operator's stop is never flipped by
// the commander: that is the same rule as retry (held_by_operator).
func (s *Server) settleIntent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Note    string   `json:"note"`
		FactIDs []string `json:"fact_ids"`
		RunIDs  []string `json:"run_ids"`
	}
	if !decodeOptionalJSON(w, r.Body, &req) {
		return
	}
	actor := model.HoldActorOperator
	if harnessSession(r) {
		actor = model.HoldActorCommander
	}
	in, ok := s.Store.GetIntent(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个意图："+id)
		return
	}
	if !settleable(in) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "意图当前是 " + in.Status + "：只有 failed / review_failed / parked，或已被停止的 pending 可以判定完成",
			"code":  "settle_conflict",
		})
		return
	}
	if in.HeldBy == model.HoldActorOperator {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "意图由操作员停止：判定完成请让操作员处理",
			"code":  "held_by_operator",
		})
		return
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		writeErr(w, http.StatusBadRequest, "判定完成必须写理由：说明为什么这一轮算达成")
		return
	}
	if len([]rune(note)) > 500 {
		writeErr(w, http.StatusBadRequest, "完成说明超过 500 字：备注要短")
		return
	}
	factIDs := uniqueRefs(req.FactIDs)
	runIDs := uniqueRefs(req.RunIDs)
	if len(factIDs)+len(runIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "判定完成至少要引用一条证据（fact_ids 或 run_ids）")
		return
	}
	db := s.Store.Snapshot()
	facts := make(map[string]model.Fact, len(factIDs))
	for _, f := range db.Facts {
		facts[f.ID] = f
	}
	verified := 0
	for _, fid := range factIDs {
		f, found := facts[fid]
		if !found {
			writeErr(w, http.StatusBadRequest, "没有这条事实："+fid)
			return
		}
		if f.Unusable() {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "事实 " + fid + " 已撤回或驳回，不能当作完成依据",
				"code":  "evidence_unusable",
			})
			return
		}
		if f.Status == "verified" {
			verified++
		}
	}
	runs := make(map[string]struct{}, len(runIDs))
	for _, run := range db.Runs {
		runs[run.ID] = struct{}{}
	}
	for _, rid := range runIDs {
		if _, found := runs[rid]; !found {
			writeErr(w, http.StatusBadRequest, "没有这次运行："+rid)
			return
		}
	}
	// With reviews off nothing ever becomes verified, so "unverified" is not a
	// defect there. With reviews on the completion is a claim until every cited
	// fact has a verdict; the surfaces show that rather than hiding it.
	unverified := false
	if !s.ReviewsDisabled() {
		unverified = len(factIDs) == 0 || verified < len(factIDs)
	}
	at := time.Now()
	cur, applied, err := s.Store.UpdateIntentIfHeld(id, in.Status, in.RunID, in.Hold, in.HeldBy, func(i *model.Intent) {
		i.Status = model.StatusDone
		i.DoneDeclaredBy = actor
		i.DoneNote = note
		i.DoneFactIDs = factIDs
		i.DoneRunIDs = runIDs
		i.DoneAt = &at
		i.Wait = nil
		i.Notes = append(i.Notes, model.IntentNote{
			At: at, Kind: model.IntentNoteSettle, Text: note, RefFact: firstRef(factIDs),
		})
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !applied {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "意图已被其他操作改变：刷新后按当前状态再判定",
			"code":  "settle_conflict",
		})
		return
	}
	s.recordAction("settle", id, note)
	// A settled direction will not consume its children's results, and settling
	// does not go through the lifecycle that normally orphans them: fail the
	// unstarted ones here, exactly as parking does.
	s.failUnstartedChildren(cur)
	// A waiting parent reads the child's terminal state, so a declared done has
	// to run the same evaluation a finished run would.
	s.pump()
	writeJSON(w, http.StatusOK, map[string]any{
		"settled": true, "intent_id": id, "done_by": actor,
		"done_unverified": unverified, "intent": cur,
	})
}

// uniqueRefs trims, drops empties and keeps the first occurrence of every id.
func uniqueRefs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func firstRef(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}
