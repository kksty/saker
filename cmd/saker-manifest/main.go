// Command saker-manifest writes the build manifest without running the gate.
//
// cmd/saker-gate calls the same collector at the end of a full run; this command is
// the standalone form for "what does this checkout currently look like?" (for
// example before starting a long test, or to regenerate a manifest after an
// artifact was rebuilt out of band). It performs no checks and no builds.
//
// Model probing is off by default here because writing a manifest on demand
// should not spend a model request; cmd/saker-gate turns it on for its end-of-run
// manifest.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/saker/internal/buildinfo"
)

func main() {
	var (
		repo          string
		harnessDir    string
		binDir        string
		harnessConfig string
		out           string
		probe         bool
		probeTimeout  time.Duration
	)
	flag.StringVar(&repo, "repo", "", "仓库根目录（默认用 git 定位当前目录）")
	flag.StringVar(&harnessDir, "harness-dir", "", "cyber-harness 源码目录（默认 $HARNESS_DIR、.build/harness-src、同父目录 cyber-harness）")
	flag.StringVar(&binDir, "bin", "bin", "二进制目录（相对仓库根）")
	flag.StringVar(&harnessConfig, "harness-config", "", "harness 配置文件（默认 $SAKER_HARNESS_CONFIG 或 ~/.cyber/cyber.yaml）")
	flag.StringVar(&out, "out", buildinfo.ManifestPath, "清单输出路径（相对仓库根）")
	flag.BoolVar(&probe, "model-probe", false, "对 active provider 发一次极小请求，记录真实 model id（默认关闭，不花钱）")
	flag.DurationVar(&probeTimeout, "model-probe-timeout", 30*time.Second, "模型探测超时")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "saker-manifest: 无法识别的参数：%v\n", flag.Args())
		os.Exit(2)
	}
	if err := run(repo, harnessDir, binDir, harnessConfig, out, probe, probeTimeout); err != nil {
		fmt.Fprintf(os.Stderr, "saker-manifest: %v\n", err)
		os.Exit(1)
	}
}

func run(repo, harnessDir, binDir, harnessConfig, out string, probe bool, probeTimeout time.Duration) error {
	root, err := resolveRoot(repo)
	if err != nil {
		return err
	}
	harness := buildinfo.ResolveHarnessSource(root, harnessDir)
	outAbs := out
	if !filepath.IsAbs(outAbs) {
		outAbs = filepath.Join(root, outAbs)
	}
	manifest, err := buildinfo.Collect(context.Background(), buildinfo.Options{
		Root:          root,
		HarnessDir:    harness.Dir,
		BinDir:        binDir,
		HarnessConfig: harnessConfig,
		ProbeModel:    probe,
		ProbeTimeout:  probeTimeout,
	})
	if err != nil {
		return err
	}
	if err := buildinfo.Write(outAbs, manifest); err != nil {
		return err
	}
	fmt.Print(buildinfo.Describe(manifest, buildinfo.ManifestRelPath(root, outAbs)))
	return nil
}

// resolveRoot finds the checkout: the explicit flag, then git from the current
// directory, then the current directory when it holds go.mod.
func resolveRoot(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	if cwd, err := os.Getwd(); err == nil {
		cmd := exec.Command("git", "rev-parse", "--show-toplevel")
		cmd.Dir = cwd
		if out, err := cmd.Output(); err == nil {
			if root := strings.TrimSpace(string(out)); root != "" {
				return root, nil
			}
		}
		if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err == nil {
			return cwd, nil
		}
	}
	return "", fmt.Errorf("无法定位仓库根：用 -repo 指定，或在本仓库根目录运行")
}
