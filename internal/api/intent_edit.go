package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kksty/saker/internal/store"
)

// editIntentRequest is a patch, not a replacement: only the fields present are
// written. Version is the edit_version the caller read; it is required so a
// stale editor cannot silently overwrite a newer text (the same CAS rule the
// context editor uses).
type editIntentRequest struct {
	Version     *uint64 `json:"version"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Eval        *string `json:"eval"`
}

// editIntent changes a direction's plan text before it is dispatched. It is
// intentionally not a general intent editor: hard premises, references, status
// and run history stay out. A direction that already has a run keeps the text
// that run was dispatched with, recoverable from its archived brief; live
// changes go through say/steer instead.
func (s *Server) editIntent(w http.ResponseWriter, r *http.Request) {
	var req editIntentRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "意图编辑请求不是有效 JSON")
		return
	}
	if req.Version == nil {
		writeErr(w, http.StatusBadRequest, "缺少 version：请先 GET 意图，带上读到的 edit_version")
		return
	}
	if req.Title == nil && req.Description == nil && req.Eval == nil {
		writeErr(w, http.StatusBadRequest, "没有可修改的字段（title / description / eval）")
		return
	}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			writeErr(w, http.StatusBadRequest, "标题不能为空")
			return
		}
		req.Title = &title
	}
	if req.Description != nil {
		description := strings.TrimSpace(*req.Description)
		req.Description = &description
	}
	if req.Eval != nil {
		eval := strings.TrimSpace(*req.Eval)
		req.Eval = &eval
	}

	in, err := s.Store.EditIntentPlan(r.PathValue("id"), *req.Version, actorOf(r), store.IntentPlanEdit{
		Title:       req.Title,
		Description: req.Description,
		Eval:        req.Eval,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrIntentEditNotFound):
			writeErr(w, http.StatusNotFound, "intent not found")
		case errors.Is(err, store.ErrIntentEditInvalid):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, store.ErrIntentNotEditable):
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": err.Error(), "code": "intent_not_editable", "intent": in,
			})
		case errors.Is(err, store.ErrIntentEditConflict):
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": err.Error(), "code": "intent_edit_conflict", "intent": in,
			})
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	fields := make([]string, 0, 3)
	if req.Title != nil {
		fields = append(fields, "标题")
	}
	if req.Description != nil {
		fields = append(fields, "说明")
	}
	if req.Eval != nil {
		fields = append(fields, "验收标准")
	}
	s.recordAction("intent_edit", in.ID, fmt.Sprintf("改%s（v%d）", strings.Join(fields, "、"), in.EditVersion))
	writeJSON(w, http.StatusOK, in)
}
