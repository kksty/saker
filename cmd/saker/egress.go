package main

// Egress status for the console: what the scan node will dial, plus one probe
// through a throwaway process using the same saved selection.
//
// The probe is deliberately sideways. Asking the live scan node would couple a
// settings read to run state, and the node has no "test the current egress"
// command; a fresh process with a probe-only document answers the operator's
// real question ("can this machine still get out through this egress?") without
// touching project data or the running hub.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kksty/saker/internal/host"
	"github.com/kksty/saker/internal/settings"
)

const (
	// A direct or single-URL probe is one small HTTPS request; a subscription
	// probe refetches the feed (up to 8 MiB) and may health-check nodes, so its
	// result lives much longer.
	egressProbeTTL    = 20 * time.Second
	egressProbeSubTTL = 2 * time.Minute
	// Budget covers process start + the clamp below; the curl call itself gets
	// its own 10s ceiling so a wedged dial cannot hold the console request.
	egressProbeBudget = 15 * time.Second
	egressProbeLimit  = "10"
	egressProbeTarget = "https://example.com/"
)

type egressProbe struct {
	OK         bool      `json:"ok"`
	HTTPCode   int       `json:"http_code,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
	At         time.Time `json:"at"`
	Target     string    `json:"target"`
}

type egressReport struct {
	settings.EgressView
	Probe      *egressProbe `json:"probe,omitempty"`
	ProbeError string       `json:"probe_error,omitempty"`
}

// egressProber owns the cached probe result and the one-at-a-time rule: a
// 30-second status poll must not spawn a process per tick, and a forced probe
// from the button must not run concurrently with one already in flight. The
// cache lock is separate from the run lock so a poll read never waits behind a
// subprocess: the chip on every page keeps answering with the last measurement
// while the panel's forced probe is still running.
type egressProber struct {
	mu      sync.Mutex
	runMu   sync.Mutex
	scanBin string
	doc     func() ([]byte, error)
	run     func(ctx context.Context, doc []byte) (egressProbe, error)
	cache   *egressProbe
	at      time.Time
}

func newEgressProber(scanBin string, doc func() ([]byte, error)) *egressProber {
	p := &egressProber{scanBin: scanBin, doc: doc}
	p.run = p.runProcess
	return p
}

func egressBinary(local *host.Host) string {
	if local == nil {
		return ""
	}
	return local.NodeBinary()
}

func (p *egressProber) ttl(mode string) time.Duration {
	if mode == "subscription" {
		return egressProbeSubTTL
	}
	return egressProbeTTL
}

// Cached returns the last result without starting work: the rail chip reads it
// on every page, and browsing a project must not probe the network.
func (p *egressProber) Cached() *egressProbe {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		return nil
	}
	cached := *p.cache
	return &cached
}

// Probe returns the cached result while it is fresh, otherwise runs one. A nil
// prober or a missing document is an error the console shows as "probe_error",
// not as a red light: an un-runnable check is not a verdict about the network.
func (p *egressProber) Probe(ctx context.Context, force bool, mode string) (*egressProbe, error) {
	if p == nil || p.doc == nil {
		return nil, errors.New("没有可用的探测配置")
	}
	if !force {
		if cached, ok := p.fresh(mode); ok {
			return cached, nil
		}
	}
	// One probe at a time. The cache lock is not held here: Cached and the
	// non-forced callers above keep reading the previous result while a process
	// runs.
	p.runMu.Lock()
	defer p.runMu.Unlock()
	if !force {
		// A probe that was already in flight when this caller arrived may have
		// refreshed the cache; reuse it instead of running a second one.
		if cached, ok := p.fresh(mode); ok {
			return cached, nil
		}
	}
	doc, err := p.doc()
	if err != nil {
		return nil, err
	}
	probe, err := p.run(ctx, doc)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.cache, p.at = &probe, time.Now()
	p.mu.Unlock()
	return &probe, nil
}

// fresh copies the cached probe when it is still within the mode's TTL.
func (p *egressProber) fresh(mode string) (*egressProbe, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil || time.Since(p.at) >= p.ttl(mode) {
		return nil, false
	}
	cached := *p.cache
	return &cached, true
}

func (p *egressProber) runProcess(ctx context.Context, doc []byte) (egressProbe, error) {
	if p.scanBin == "" {
		return egressProbe{}, errors.New("本机宿主没有可用的 scan 二进制")
	}
	dir, err := os.MkdirTemp("", "saker-egress-*")
	if err != nil {
		return egressProbe{}, errors.New("无法建立探测临时目录")
	}
	defer os.RemoveAll(dir)
	config := filepath.Join(dir, "egress.yaml")
	if err := os.WriteFile(config, doc, 0o600); err != nil {
		return egressProbe{}, errors.New("无法写入探测配置")
	}
	ctx, cancel := context.WithTimeout(ctx, egressProbeBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.scanBin, "--config", config,
		"curl", "-s", "-o", os.DevNull, "-m", egressProbeLimit,
		"-w", "%{http_code}", egressProbeTarget)
	// cwd is the throwaway directory so the scan writes its .cyber there; the
	// probe document carries no data_dir, so it cannot reach the operator's.
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	runErr := cmd.Run()
	probe := egressProbe{
		DurationMS: time.Since(start).Milliseconds(),
		At:         time.Now(),
		Target:     egressProbeTarget,
	}
	if code, convErr := strconv.Atoi(strings.TrimSpace(stdout.String())); convErr == nil {
		probe.HTTPCode = code
	}
	if runErr != nil {
		probe.Error = firstLine(stderr.String())
		if probe.Error == "" {
			probe.Error = firstLine(runErr.Error())
		}
		return probe, nil
	}
	probe.OK = probe.HTTPCode >= 200 && probe.HTTPCode < 400
	if !probe.OK {
		probe.Error = fmt.Sprintf("探测目标返回 HTTP %d", probe.HTTPCode)
	}
	return probe, nil
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if line, _, ok := strings.Cut(text, "\n"); ok {
		text = strings.TrimSpace(line)
	}
	if len(text) > 240 {
		text = text[:240] + "…"
	}
	return text
}

// serveEgress reports the saved selection plus the last probe. Only ?probe=1
// starts a probe (the 检测 button, and the settings panel once on mount); every
// other read is cache-only, so the rail chip shown on every page never turns
// browsing into continuous outbound traffic.
//
// Operator-only by construction: agentRouteHidden hides every unlisted /api/
// segment, so a harness session gets the same 404 it gets for /api/settings.
func (h *hub) serveEgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只支持 GET"})
		return
	}
	if h.settings == nil {
		writeJSON(w, http.StatusOK, egressReport{
			EgressView: settings.EgressView{Mode: "unknown"},
			ProbeError: "本实例没有可读的设置存储",
		})
		return
	}
	view := h.settings.EgressView()
	report := egressReport{EgressView: view}
	var probe *egressProbe
	var err error
	if r.URL.Query().Get("probe") == "1" {
		probe, err = h.egress.Probe(r.Context(), true, view.Mode)
	} else {
		probe = h.egress.Cached()
	}
	switch {
	case err != nil:
		report.ProbeError = h.redactEgress(err.Error())
	case probe != nil:
		safe := *probe
		safe.Error = h.redactEgress(safe.Error)
		report.Probe = &safe
	}
	writeJSON(w, http.StatusOK, report)
}

// redactEgress keeps stored credentials out of probe output; the scan's own
// error may echo the upstream URL.
func (h *hub) redactEgress(text string) string {
	if text == "" || h.settings == nil {
		return text
	}
	for _, secret := range h.settings.SecretValues() {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "***")
		}
	}
	return text
}
