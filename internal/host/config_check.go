package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ScanConfigCheck runs the local scan binary's offline config command against
// one candidate document. ok=false means the scan rejected it and detail is the
// scan's own one-line explanation (empty when it printed none); a non-nil error
// means the check could not run at all.
//
// Candidate values only travel through a temporary file, never via argv or logs.
func (h *Host) ScanConfigCheck(ctx context.Context, candidate []byte) (ok bool, detail string, err error) {
	f, err := os.CreateTemp("", "saker-scan-config-*.yaml")
	if err != nil {
		return false, "", errors.New("无法建立临时配置校验文件")
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(candidate)
	if closeErr := f.Close(); writeErr != nil || closeErr != nil {
		return false, "", errors.New("无法写入临时配置校验文件")
	}
	cmd := exec.CommandContext(ctx, h.cfg.NodeBin, "--config", f.Name(), "config", "validate")
	cmd.Dir = filepath.Dir(f.Name())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return false, "", errors.New("执行节点校验超时或取消")
		}
		// A missing/unexecutable scan binary is not a configuration verdict:
		// report it as a check failure instead of blaming the candidate.
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return false, "", errors.New("无法运行执行节点校验")
		}
		return false, configProblem(stderr.String()), nil
	}
	return true, "", nil
}

// ValidateScanConfig is the guardrail save's check: the scan either accepts the
// candidate or the caller reports the section that has to be looked at.
func (h *Host) ValidateScanConfig(ctx context.Context, candidate []byte) error {
	ok, _, err := h.ScanConfigCheck(ctx, candidate)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("执行节点未通过配置校验，请检查保存的守门策略与 JEV 字段")
	}
	return nil
}

// configProblem keeps the scan's own explanation: it names the instance and the
// field the harness rejected, which is what the settings form has to show. The
// caller redacts credentials before displaying it.
func configProblem(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "error: "))
		if line == "" {
			continue
		}
		if len(line) > 300 {
			line = line[:300] + "…"
		}
		return line
	}
	return ""
}
