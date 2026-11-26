package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/kksty/saker/internal/settings"
)

func (h *hub) serveGuardrailSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只支持 PUT"})
		return
	}
	var in settings.GuardrailInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "守门策略请求格式无效"})
		return
	}

	h.settingsApplyMu.Lock()
	defer h.settingsApplyMu.Unlock()
	var validate func([]byte) error
	if localHost := h.cfg.LocalHost; localHost != nil {
		validate = func(candidate []byte) error {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			return localHost.ValidateScanConfig(ctx, candidate)
		}
	}
	changed, err := h.settings.SaveGuardrail(in, validate)
	if err != nil {
		if errors.Is(err, settings.ErrGuardrailConfig) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法保存守门策略配置"})
		}
		return
	}
	view := h.settings.View()
	if changed {
		view.Applied, view.RequiresRestart, view.RestartReason, err = h.applyModelPath(h.settings.Effective(), false, "守门策略在本机执行节点启动时加载")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":    "守门策略已保存，准备宿主运行配置失败",
				"code":     "settings_saved_apply_failed",
				"settings": view,
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, view)
}
