package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kksty/saker/internal/model"
)

var (
	// ErrIntentEditNotFound means the id is unknown in this project's ledger.
	ErrIntentEditNotFound = errors.New("意图不存在")
	// ErrIntentNotEditable means the direction already has a run. Its plan text
	// is no longer only this row: every dispatch archived the brief it used, so
	// changing the row afterwards would split "what was asked" from "what ran".
	// The first version therefore edits only a never-dispatched pending intent.
	ErrIntentNotEditable = errors.New("意图已有运行记录，计划正文不能再改")
	// ErrIntentEditConflict means the editor saved a version it no longer holds.
	ErrIntentEditConflict = errors.New("意图计划正文版本已变化")
	// ErrIntentEditInvalid means the edit would leave an empty title.
	ErrIntentEditInvalid = errors.New("意图计划正文无效")
)

// IntentPlanEdit is a patch of the pre-dispatch plan text. A nil field means
// "leave it as it is": an editor correcting the acceptance criterion must not
// have to resend (and risk dropping) the description.
type IntentPlanEdit struct {
	Title       *string
	Description *string
	Eval        *string
}

// EditIntentPlan writes one text edit under an explicit version check. It is
// deliberately narrower than UpdateIntent: only title/description/eval, only a
// pending direction that no run has ever claimed, and only the version the
// caller read.
//
// The run row stamps IntentVersion in the same claim transaction
// (startIntentRunLocked). The two writes serialize on the store lock:
//
//   - an edit that lands first bumps the text version, and the next dispatch
//     stamps the bumped version;
//   - a dispatch that lands first sets RunID/Attempts, so the edit's gate fails
//     and the run keeps the version it read.
//
// There is no edit history here. A dispatched direction's text is recoverable
// from that run's archived brief; pre-dispatch drafts that were never dispatched
// were never shown to an executor.
func (s *Store) EditIntentPlan(id string, expected uint64, by string, edit IntentPlanEdit) (model.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intentLocked(id)
	if !ok {
		return model.Intent{}, ErrIntentEditNotFound
	}
	if in.Status != model.StatusPending || in.RunID != "" || in.Attempts != 0 {
		reason := "当前状态 " + in.Status + "；只允许尚未派发的 pending 意图"
		if in.Status == model.StatusPending {
			reason = "已有运行记录；只允许尚未派发的意图"
		}
		return detachIntent(*in), fmt.Errorf("%w：%s", ErrIntentNotEditable, reason)
	}
	if in.EditVersion != expected {
		return detachIntent(*in), fmt.Errorf("%w：当前 v%d（请求 v%d）", ErrIntentEditConflict, in.EditVersion, expected)
	}
	if edit.Title != nil {
		title := strings.TrimSpace(*edit.Title)
		if title == "" {
			return detachIntent(*in), fmt.Errorf("%w：标题不能为空", ErrIntentEditInvalid)
		}
		in.Title = title
	}
	if edit.Description != nil {
		in.Description = strings.TrimSpace(*edit.Description)
	}
	if edit.Eval != nil {
		in.Eval = strings.TrimSpace(*edit.Eval)
	}
	in.EditVersion++
	in.EditedBy = by
	in.UpdatedAt = time.Now()
	if err := s.flush("intent"); err != nil {
		return model.Intent{}, err
	}
	return detachIntent(*in), nil
}
