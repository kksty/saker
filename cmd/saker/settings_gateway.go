package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/kksty/saker/internal/settings"
)

// serveGatewaySettings edits the tool API gateway: the `gateway` section of
// cyber.yaml (external upstreams, kinds, enabled flags, credentials) plus
// `cyberhub.proxy`. The scan reads both at process start, so a changed save
// always takes the restart path; there is no hot-apply for this section.
func (h *hub) serveGatewaySettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只支持 PUT"})
		return
	}
	var in settings.GatewayInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "工具 API 网关请求格式无效"})
		return
	}

	h.settingsApplyMu.Lock()
	defer h.settingsApplyMu.Unlock()
	// The running scan binary is the authority on what this node can load: the
	// console only knows the shape. A rejection keeps its own instance/field
	// explanation; SaveGateway redacts credentials from it.
	var validate func([]byte) error
	if localHost := h.cfg.LocalHost; localHost != nil {
		validate = func(candidate []byte) error {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			ok, detail, err := localHost.ScanConfigCheck(ctx, candidate)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
			if detail == "" {
				detail = "执行节点没有给出原因"
			}
			return errors.New("执行节点拒绝了网关配置：" + detail)
		}
	}
	changed, err := h.settings.SaveGateway(in, validate)
	if err != nil {
		if errors.Is(err, settings.ErrGatewayConfig) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法保存工具 API 网关配置"})
		}
		return
	}
	view := h.settings.View()
	if changed {
		view.Applied, view.RequiresRestart, view.RestartReason, err = h.applyModelPath(
			h.settings.Effective(), false,
			"工具 API 网关只在 scan 启动时读取，需要重启本机宿主后生效")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":    "工具 API 网关已保存，准备宿主运行配置失败",
				"code":     "settings_saved_apply_failed",
				"settings": view,
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, view)
}
