package buildinfo

// Model connection and the real upstream model id.
//
// The historical ledgers only ever recorded the gateway aggregate name
// ("group/smart-split"), so a result could not be attributed to an actual
// upstream model. The manifest therefore reads the active harness profile and,
// when probing is enabled, sends one minimal request and records the model id the
// response itself carried.
//
// The API key is only ever used as a request header. It never enters the manifest:
// the probe records a boolean "an API key is set" and redacts every known secret
// from the error text it keeps.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kksty/saker/internal/modelclient"
	"github.com/kksty/saker/internal/settings"
)

// defaultProbeTimeout bounds the one model request.
const defaultProbeTimeout = 30 * time.Second

// anthropicProbeVersion mirrors the Messages API version modelclient sends. It is
// repeated here because the probe is deliberately a two-field envelope reader
// (status + model id), not a second chat client.
const anthropicProbeVersion = "2023-06-01"

// collectModel reads the active harness profile and optionally probes it.
func collectModel(ctx context.Context, root string, opts Options) ModelInfo {
	path := strings.TrimSpace(opts.HarnessConfig)
	if path != "" {
		path = absFrom(root, path)
	} else {
		path = settings.HarnessConfigPath()
	}
	mi := ModelInfo{}
	if path == "" {
		mi.Error = ptr("无法确定 harness 配置路径：没有 home 目录且未设置 SAKER_HARNESS_CONFIG")
		return mi
	}
	mi.ConfigPath = manifestPath(root, path)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		mi.ConfigPresent = false
		if opts.ProbeModel {
			mi.Probe = skippedProbe("no_harness_config")
		}
		return mi
	}
	mi.ConfigPresent = true

	// Empty legacy path: the gate records the harness profile, not a data
	// directory's pre-harness settings.json (a missing file is not an error).
	store, err := settings.LoadAt("", path)
	if err != nil {
		mi.Error = ptr(clipText(err.Error(), 400))
		if opts.ProbeModel {
			mi.Probe = skippedProbe("config_error")
		}
		return mi
	}
	m := store.Effective()
	if active := store.Profile(); active != "" {
		mi.ActiveProfile = ptr(active)
	}
	if m.Provider != "" {
		mi.Provider = ptr(m.Provider)
	}
	if m.Model != "" {
		mi.ModelID = ptr(m.Model)
	}
	mi.APIKeyPresent = strings.TrimSpace(m.APIKey) != ""
	protocol := ""
	if m.BaseURL != "" {
		_, protocol = modelclient.Endpoint(m.BaseURL, m.Provider)
	}
	if protocol != "" {
		mi.Protocol = ptr(protocol)
	}
	if host, basePath, err := sanitizeBaseURL(m.BaseURL); err != nil {
		mi.Error = ptr(err.Error())
	} else if host != "" {
		mi.BaseURL = ptr(host)
		if basePath != "" && basePath != "/" {
			mi.BaseURLPath = ptr(basePath)
		}
	}
	if herr := store.HarnessError(); herr != nil {
		mi.Error = ptr(clipText(herr.Error(), 400))
	}

	if opts.ProbeModel {
		mi.Probe = runModelProbe(ctx, m.BaseURL, protocol, m.Model, m.APIKey, store.SecretValues(), opts.ProbeTimeout)
	}
	return mi
}

// skippedProbe records why no request was sent.
func skippedProbe(reason string) *ProbeInfo {
	return &ProbeInfo{Probed: false, Available: false, Reason: &reason}
}

// sanitizeBaseURL reduces a configured base URL to scheme://host plus a path.
// Userinfo, query and fragment are dropped: the manifest stays a reproducibility
// record, not a credential store. "" for an unset URL is not an error.
func sanitizeBaseURL(raw string) (host, path string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("base_url 不是合法 URL（缺少 scheme 或 host）")
	}
	return u.Scheme + "://" + u.Host, u.EscapedPath(), nil
}

// runModelProbe sends one minimal chat/completions (or Messages) request. It
// never returns an error that fails the gate: a missing config, missing key, bad
// URL or unreachable provider is a recorded reason, not a check failure.
func runModelProbe(ctx context.Context, baseURL, protocol, model, apiKey string, secrets []string, timeout time.Duration) *ProbeInfo {
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	switch {
	case strings.TrimSpace(baseURL) == "":
		return skippedProbe("no_base_url")
	case strings.TrimSpace(model) == "":
		return skippedProbe("no_model_id")
	case strings.TrimSpace(apiKey) == "":
		return skippedProbe("no_api_key")
	}
	endpointBase, resolvedProtocol := modelclient.Endpoint(baseURL, protocol)
	endpointBase = stripUserInfo(endpointBase)
	if resolvedProtocol == "" {
		resolvedProtocol = modelclient.ProviderOpenAI
	}

	start := time.Now()
	result := sendProbe(ctx, endpointBase, resolvedProtocol, model, apiKey, timeout)
	p := &ProbeInfo{Probed: true, LatencyMS: time.Since(start).Milliseconds()}
	if result.status > 0 {
		p.HTTPStatus = &result.status
	}
	if result.finish != "" {
		p.FinishReason = &result.finish
	}
	if result.upstreamModel != "" {
		p.UpstreamModel = ptr(result.upstreamModel)
	}
	if result.upstreamProvider != "" {
		p.UpstreamProvider = ptr(result.upstreamProvider)
	}
	if result.err != "" {
		redacted := redactSecrets(result.err, secrets)
		p.Error = &redacted
		return p
	}
	p.Available = true
	if result.model != "" {
		p.ReturnedModel = ptr(result.model)
	}
	return p
}

// probeResult is what the minimal probe learned.
type probeResult struct {
	status           int
	model            string
	finish           string
	upstreamModel    string
	upstreamProvider string
	err              string
}

// sendProbe performs the request and reads only the envelope fields the manifest
// needs. The response is deliberately NOT validated for content: max_tokens=1
// truncates the answer, and that is fine — the model id is the point.
func sendProbe(ctx context.Context, endpointBase, protocol, model, apiKey string, timeout time.Duration) probeResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		endpoint string
		body     []byte
		req      *http.Request
		err      error
	)
	if protocol == modelclient.ProviderAnthropic {
		endpoint = endpointBase + "/messages"
		body, err = json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		})
	} else {
		endpoint = endpointBase + "/chat/completions"
		body, err = json.Marshal(map[string]any{
			"model":       model,
			"messages":    []map[string]string{{"role": "user", "content": "ping"}},
			"max_tokens":  1,
			"temperature": 0,
		})
	}
	if err != nil {
		return probeResult{err: "构造探针请求失败：" + err.Error()}
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return probeResult{err: "构造探针请求失败：" + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if protocol == modelclient.ProviderAnthropic {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", anthropicProbeVersion)
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	modelclient.ApplyEndpointHeaders(req, probeSessionID())

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return probeResult{err: fmt.Sprintf("模型请求失败（%s）：%v", endpoint, err)}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return probeResult{status: resp.StatusCode, err: "读取模型响应失败：" + readErr.Error()}
	}
	// Attribution headers are read before any return so a gateway that reports
	// the real upstream model there is still recorded on an error response.
	// A gateway may report the real id here; others put it in the body's model
	// field, which is captured separately.
	result := probeResult{
		status:           resp.StatusCode,
		upstreamModel:    resp.Header.Get("X-Magpie-Model"),
		upstreamProvider: resp.Header.Get("X-Magpie-Provider"),
	}
	if resp.StatusCode != http.StatusOK {
		result.err = fmt.Sprintf("模型返回 HTTP %d：%s", resp.StatusCode, clipText(string(raw), 300))
		return result
	}
	if protocol == modelclient.ProviderAnthropic {
		var out struct {
			Model      string `json:"model"`
			StopReason string `json:"stop_reason"`
			Error      *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			result.err = "模型响应不是 JSON：" + err.Error()
			return result
		}
		if out.Error != nil && out.Error.Message != "" {
			result.err = "模型报错：" + out.Error.Message
			return result
		}
		result.model, result.finish = out.Model, out.StopReason
		return result
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		result.err = "模型响应不是 JSON：" + err.Error()
		return result
	}
	if out.Error != nil && out.Error.Message != "" {
		result.err = "模型报错：" + out.Error.Message
		return result
	}
	result.model = out.Model
	if len(out.Choices) > 0 {
		result.finish = out.Choices[0].FinishReason
	}
	return result
}

// probeSessionID gives the endpoint a conversation identity for gateways that
// route on it (the opencode-style header applied by ApplyEndpointHeaders).
func probeSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "saker-gate-probe"
	}
	return "saker-gate-probe-" + hex.EncodeToString(b[:])
}

// stripUserInfo removes credentials embedded in a URL before it can reach an
// error string.
func stripUserInfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

// redactSecrets replaces every known credential value (raw and JSON-escaped)
// with ***, longest first. This is the only place the probe's error text is
// allowed to become manifest content.
func redactSecrets(s string, secrets []string) string {
	values := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		values = append(values, secret)
		if escaped, err := json.Marshal(secret); err == nil {
			values = append(values, strings.Trim(string(escaped), `"`))
		}
	}
	// Longest first: a shorter value must not split a longer one.
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if len(values[j]) > len(values[i]) {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		s = strings.ReplaceAll(s, value, "***")
	}
	return s
}
