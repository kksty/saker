package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// ValidateScanConfig uses the local scan binary's offline config command.
// Candidate values only travel through a temporary file, never via argv or logs.
func (h *Host) ValidateScanConfig(ctx context.Context, candidate []byte) error {
	f, err := os.CreateTemp("", "saker-scan-config-*.yaml")
	if err != nil {
		return errors.New("无法建立临时配置校验文件")
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(candidate)
	if closeErr := f.Close(); writeErr != nil || closeErr != nil {
		return errors.New("无法写入临时配置校验文件")
	}
	cmd := exec.CommandContext(ctx, h.cfg.NodeBin, "--config", f.Name(), "config", "validate")
	cmd.Dir = filepath.Dir(f.Name())
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return errors.New("执行节点校验超时或取消")
		}
		return errors.New("执行节点未通过配置校验，请检查保存的守门策略与 JEV 字段")
	}
	return nil
}
