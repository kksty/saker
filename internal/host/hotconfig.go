package host

// Configuration hot update against a running harness.
//
// The supervisor's Update() restarts the child so it re-reads its --config file.
// The harness also exposes ConfigService/UpdateConfig, which commits the shared
// configuration (llm/agent/traffic) and broadcasts a reload to every connected
// scan. A model-only settings save uses this path and only falls back to Update()
// when the host is offline, rejects the document, or does not report the new
// provider/model within the bounded wait.
//
// The wait is a read-only confirmation, not a retry: the config was already
// committed by the host. A timeout means "the running agents did not show the
// new binding", which is exactly when the restart fallback is the honest answer.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kksty/saker/internal/aop"
)

// HotApplyTimeout bounds how long the control plane waits for the running host
// to report the pushed provider/model. The config commit itself is usually
// immediate; a scan reload that has not answered within this window should take
// the restart path instead of leaving the console waiting.
const HotApplyTimeout = 10 * time.Second

// hotApplyPoll is the ListAgents confirmation interval. It is a variable so the
// tests can shorten the window without sleeping for the real timeout.
var hotApplyPoll = 250 * time.Millisecond

// PushRuntimeConfig commits one shared configuration document to the running
// harness and waits until its agent reports the expected provider/model. config
// is the DistributeConfig object ({"llm": {...}}); agentID pins the local agent
// when the host serves more than one.
//
// The returned error distinguishes the stages: an UpdateConfig rejection is
// returned as-is (the operator needs the host's reason), while an unmet
// confirmation is reported with the last observed binding so the caller can log
// why it fell back to a restart.
func PushRuntimeConfig(ctx context.Context, endpoint, token string, config map[string]any, agentID, wantProvider, wantModel string, timeout time.Duration) error {
	client := aop.New(endpoint, token)
	if _, err := client.UpdateConfig(ctx, config); err != nil {
		return fmt.Errorf("宿主拒绝热更新：%w", err)
	}
	if timeout <= 0 {
		timeout = HotApplyTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// lastBinding is the most recent successful read; lastErr is the fallback
	// when no read ever succeeded. A read that races the deadline must not hide
	// the binding an earlier poll already observed.
	lastBinding, lastErr := "", ""
	for {
		agents, err := client.Agents(waitCtx)
		if err == nil {
			agent, found := aop.Find(agents, agentID)
			switch {
			case !found:
				lastBinding = "宿主未注册本机执行者"
			case agent.ConfigError != "":
				lastBinding = "宿主报告配置错误：" + agent.ConfigError
			case bindingMatches(agent.Provider, wantProvider) && bindingMatches(agent.Model, wantModel):
				return nil
			default:
				lastBinding = fmt.Sprintf("宿主当前绑定 %s/%s", agent.Provider, agent.Model)
			}
			lastErr = ""
		} else if lastBinding == "" {
			lastErr = "读取宿主 agent 失败：" + err.Error()
		}
		select {
		case <-waitCtx.Done():
			reason := lastBinding
			if reason == "" {
				reason = lastErr
			}
			if reason == "" {
				reason = waitCtx.Err().Error()
			}
			return fmt.Errorf("等待宿主应用模型配置超时（%s）：%s", timeout, reason)
		case <-time.After(hotApplyPoll):
		}
	}
}

// bindingMatches compares one binding field. An empty want means "not asserted":
// the runtime file may legitimately leave provider blank (the host infers it
// from the base URL), so only a value the caller asked for is enforced.
func bindingMatches(got, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(got), want)
}

// StageConfig records the environment/flags a future launch must use without
// touching the live child. The hot-update path commits the new configuration to
// the running host, so the control plane must not restart it; the pending
// values still have to be in place for a crash restart.
func (h *Host) StageConfig(env, hubArgs, nodeArgs []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.Env = append([]string(nil), env...)
	h.cfg.HubArgs = append([]string(nil), hubArgs...)
	h.cfg.Args = append([]string(nil), nodeArgs...)
}
