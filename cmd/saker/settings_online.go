package main

// Settings connectivity through the live host.
//
// The console's "test connection" should measure the component that will use the
// connection: the running harness. Its ConfigService knows the effective
// runtime, its provider normalisation and its proxy, and its ListModels speaks
// the same endpoint the model calls use. A direct in-process probe remains the
// fallback for an audit instance or a host that is still starting.

import (
	"context"
	"log"

	"github.com/kksty/saker/internal/settings"
)

// settingsOnlineLink is the running local host's management endpoint. Empty ok
// means the console is not attached to a live local host, so host-side probes
// are not available.
func (h *hub) settingsOnlineLink() (base, token, agent string, ok bool) {
	local := h.cfg.LocalHost
	if local == nil {
		return "", "", "", false
	}
	if !local.Status().Online {
		return "", "", "", false
	}
	return local.Endpoint(), local.Token(), local.Agent(), true
}

// checkModel is the settings page's connectivity probe: host TestLLM/ListModels
// when the host is live, the direct probe otherwise. A transport failure falls
// back; a definitive host rejection (invalid_argument with the host's reason) is
// returned as the probe answer so a bad key is not masked by a second attempt.
func (h *hub) checkModel(ctx context.Context) settings.ModelProbe {
	if h.settings == nil {
		return settings.ModelProbe{Error: "设置存储未初始化"}
	}
	if base, token, _, ok := h.settingsOnlineLink(); ok {
		probe, err := h.settings.CheckModelOnline(ctx, base, token)
		if err == nil {
			return probe
		}
		log.Printf("settings: 宿主连通性检测失败，回退自探测：%v", err)
	}
	probe := h.settings.CheckModel(ctx)
	probe.Source = "self"
	return probe
}

// onlineModels asks the live host for its model catalogue. ok=false means the
// caller should use its own probe (host absent, no catalogue support, or the
// call failed): an empty list is not a successful answer.
func (h *hub) onlineModels(ctx context.Context) (models []string, ok bool) {
	if h.settings == nil {
		return nil, false
	}
	base, token, _, linked := h.settingsOnlineLink()
	if !linked {
		return nil, false
	}
	models, supported, err := h.settings.ModelsOnline(ctx, base, token)
	if err != nil {
		log.Printf("settings: 宿主模型列表失败，回退自探测：%v", err)
		return nil, false
	}
	if !supported || len(models) == 0 {
		return nil, false
	}
	return models, true
}
