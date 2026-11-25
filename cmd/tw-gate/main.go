// Command tw-gate is the single local gate for tianwu.
//
// It is the one place the checks live. scripts/gate.ps1 and scripts/gate.sh are
// thin entries that locate the repository root and run this command, so Windows
// and Linux execute the same list against the same implementation — one platform
// cannot quietly lose a check (taskbook P0-1: "单一检查实现 + 两个薄入口").
//
// The steps run serially and each one prints its elapsed time:
//
//  1. go build ./cmd/... ./internal/...
//  2. go vet ./cmd/... ./internal/...
//  3. go test ./cmd/... ./internal/... -count=1 (plus -test-flags, default -timeout 20m)
//  4. frontend: pnpm --dir web typecheck / test / build (unless -skip-web)
//  5. embedded-artifact freshness: internal/web/static must not gain differences
//     the rebuild did not have before; a pre-existing dirty tree is not a failure
//  6. node scripts/check-docs.mjs
//  7. AOP generated-code drift: vendored internal/aop/* generated files must
//     match the harness source after normalization; a missing harness checkout
//     is recorded as a skip, not a failure
//  8. harness: build cyber-harness + cyber-scan from the independent checkout
//     (unless -skip-harness)
//  9. control plane: build bin/tw and bin/tw-repair
//  10. build manifest (.build/build-manifest.json) — source, binaries, versions,
//     the vendored AOP fingerprint, platform/toolchain, and the real upstream
//     model id when probing is on
//
// -manifest-only writes the manifest without running any check or build;
// -check-aop-protos runs only step 7; -sync-aop-protos replaces the vendored
// files from the harness checkout and then runs step 7 (the scripts of the same
// names are thin entries to those two modes).
//
// The gate fails on the first failing step: a check that did not run has no
// result to report, and the embedded-artifact comparison is only meaningful when
// the frontend build before it succeeded.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kksty/tianwu/internal/buildinfo"
)

// goproxyDefault mirrors scripts/build-harness.*: a fresh Linux run has no
// module cache yet, and the project documents this mirror as its default.
const goproxyDefault = "https://goproxy.cn,direct"

// goPackages is the pattern set every Go step compiles, vets and tests.
//
// It is deliberately not "./...": the default data directory is ./data, inside
// the module root, and a test engagement stores the target's own source files
// under data/projects/<id>/cases/<case>/raw/. Go treats those as packages, so
// "./..." fails on exactly the machines that have run a real project — the
// gate would be red for having been used. Every package this repository owns
// lives under cmd/ or internal/; TestGoPackagesCoverTheModule fails if one
// appears elsewhere.
var goPackages = []string{"./cmd/...", "./internal/..."}

// config is the resolved invocation.
type config struct {
	skipWeb        bool
	skipHarness    bool
	harnessDir     string
	binDir         string
	testFlags      string
	manifestOut    string
	manifestOnly   bool
	modelProbe     bool
	noModelProbe   bool
	modelProbeWait time.Duration
	harnessConfig  string
	syncAOPProtos  bool
	checkAOPProtos bool
	aopDryRun      bool
}

func main() {
	var cfg config
	flag.BoolVar(&cfg.skipWeb, "skip-web", false, "显式跳过前端检查（typecheck/test/build 与嵌入产物新鲜度）；默认不跳过")
	flag.BoolVar(&cfg.skipHarness, "skip-harness", false, "显式跳过 harness 构建；默认不跳过，源码缺失时失败并给出 clone 指引")
	flag.StringVar(&cfg.harnessDir, "harness-dir", "", "cyber-harness 源码目录（默认依次：$HARNESS_DIR、.build/harness-src、同父目录的 cyber-harness）")
	flag.StringVar(&cfg.binDir, "bin", "bin", "二进制输出目录（harness 与 tw/tw-repair 共用）")
	flag.StringVar(&cfg.testFlags, "test-flags", "-timeout 20m", "透传给 go test 的参数（默认 \"-timeout 20m\"）")
	flag.StringVar(&cfg.manifestOut, "manifest-out", buildinfo.ManifestPath, "构建清单输出路径")
	flag.BoolVar(&cfg.manifestOnly, "manifest-only", false, "只生成构建清单，不执行任何检查与构建")
	flag.BoolVar(&cfg.modelProbe, "model-probe", true, "对 active provider 发一次极小请求以记录真实 model id（默认开启；配置缺失或探测失败不使 gate 失败）")
	flag.BoolVar(&cfg.noModelProbe, "no-model-probe", false, "关闭模型探测（与 -model-probe=false 等价）")
	flag.DurationVar(&cfg.modelProbeWait, "model-probe-timeout", 30*time.Second, "模型探测超时")
	flag.StringVar(&cfg.harnessConfig, "harness-config", "", "harness 配置文件（默认 $TW_HARNESS_CONFIG 或 ~/.cyber/cyber.yaml）")
	flag.BoolVar(&cfg.syncAOPProtos, "sync-aop-protos", false, "从 harness 源同步 vendored 的 AOP 生成文件到 internal/aop（scripts/sync-aop-protos.* 的单一实现），随后运行一致性检查")
	flag.BoolVar(&cfg.checkAOPProtos, "check-aop-protos", false, "只运行 AOP 生成代码与 harness 源的一致性检查；harness 源缺失时记录 skip 且不失败")
	flag.BoolVar(&cfg.aopDryRun, "aop-dry-run", false, "与 -sync-aop-protos 一起用：只列出将写入的文件，不改动工作树")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "tw-gate: 无法识别的参数：%v\n", flag.Args())
		os.Exit(2)
	}
	if cfg.syncAOPProtos {
		exitUnlessNil(runSyncAOPProtos(cfg))
		return
	}
	if cfg.checkAOPProtos {
		exitUnlessNil(runCheckAOPProtos(cfg))
		return
	}
	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "\ntw-gate: %v\n", err)
		os.Exit(1)
	}
}

// exitUnlessNil prints one top-level error and exits 1, so the early-returning
// modes (-sync-aop-protos, -check-aop-protos) report exactly like a gate run.
func exitUnlessNil(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "tw-gate: %v\n", err)
	os.Exit(1)
}

// step is one serial check. Closures keep the numbering honest when optional
// groups are skipped.
type step struct {
	name string
	run  func() error
}

// run executes the gate.
func run(cfg config) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	exe := buildinfo.ExecutableSuffix()
	goos, goarch := buildinfo.TargetPlatform()
	probe := cfg.modelProbe && !cfg.noModelProbe

	started := time.Now()
	fmt.Printf("tw-gate: 仓库 %s\n", root)
	fmt.Printf("tw-gate: 目标平台 %s/%s；跳过前端=%v 跳过 harness=%v 模型探测=%v\n",
		goos, goarch, cfg.skipWeb, cfg.skipHarness, probe)
	if cfg.manifestOnly {
		return writeManifestOnly(cfg, root)
	}

	if err := preflight(cfg, root); err != nil {
		return err
	}

	binDirAbs := absFrom(root, cfg.binDir)
	if err := os.MkdirAll(binDirAbs, 0o755); err != nil {
		return fmt.Errorf("创建二进制目录 %s：%w", binDirAbs, err)
	}

	harness := buildinfo.ResolveHarnessSource(root, cfg.harnessDir)
	var embedded *buildinfo.EmbeddedArtifacts
	var aopProto *buildinfo.AOPProtoInfo

	steps := []step{
		{name: "go build " + strings.Join(goPackages, " "), run: func() error {
			return runCommand(root, nil, "go", append([]string{"build"}, goPackages...)...)
		}},
		{name: "go vet " + strings.Join(goPackages, " "), run: func() error {
			return runCommand(root, nil, "go", append([]string{"vet"}, goPackages...)...)
		}},
		{name: "go test " + strings.Join(goPackages, " ") + " -count=1 " + strings.TrimSpace(cfg.testFlags), run: func() error {
			args := []string{"test", "-count=1"}
			args = append(args, strings.Fields(cfg.testFlags)...)
			args = append(args, goPackages...)
			return runCommand(root, nil, "go", args...)
		}},
	}
	if !cfg.skipWeb {
		steps = append(steps,
			step{name: "web: pnpm --dir web typecheck", run: func() error {
				return runCommand(root, nil, "pnpm", "--dir", "web", "typecheck")
			}},
			step{name: "web: pnpm --dir web test", run: func() error {
				return runCommand(root, nil, "pnpm", "--dir", "web", "test")
			}},
			step{name: "web: pnpm --dir web build + 嵌入产物新鲜度", run: func() error {
				artifacts, err := buildinfo.CheckEmbeddedFreshness(root, func() error {
					return runCommand(root, nil, "pnpm", "--dir", "web", "build")
				})
				if err != nil {
					return err
				}
				embedded = artifacts
				if len(artifacts.Changed) > 0 {
					return fmt.Errorf("嵌入产物落后于前端源码：本次 pnpm build 让 %s 产生了新差异。"+
						"请把重新构建的 %s 一并提交。变化文件：%s",
						artifacts.Dir, artifacts.Dir, buildinfo.FormatChangedPaths(artifacts.Changed, 20))
				}
				return nil
			}},
		)
	}
	steps = append(steps, step{name: "node scripts/check-docs.mjs", run: func() error {
		return runCommand(root, nil, "node", "scripts/check-docs.mjs")
	}})

	// The vendored AOP generated code is a copy of the harness protocol
	// definitions; compare it with the source before building anything from it.
	// A missing harness checkout is a recorded skip (the harness build step
	// below still owns the "source is required" rule when it is not skipped).
	steps = append(steps, step{name: "AOP 生成代码漂移检查（vendored vs harness 源）", run: func() error {
		info, err := runAOPProtoCheck(root, harness)
		aopProto = info
		if err != nil {
			return err
		}
		fmt.Printf("（%d 个文件，fingerprint=%s）", len(info.Files), info.SHA256)
		return nil
	}})

	if !cfg.skipHarness {
		steps = append(steps, step{name: "harness: 构建 cyber-harness + cyber-scan", run: func() error {
			if !harness.Found {
				return errors.New(buildinfo.HarnessCloneHint(root))
			}
			return buildHarness(harness.Dir, binDirAbs, goos, exe)
		}})
	} else {
		fmt.Printf("tw-gate: 跳过 harness 构建（-skip-harness）；清单仍会记录已有二进制\n")
	}

	steps = append(steps,
		step{name: "go build bin/tw + bin/tw-repair", run: func() error {
			if err := runCommand(root, nil, "go", "build", "-o", filepath.Join(binDirAbs, "tw"+exe), "./cmd/tw"); err != nil {
				return err
			}
			return runCommand(root, nil, "go", "build", "-o", filepath.Join(binDirAbs, "tw-repair"+exe), "./cmd/tw-repair")
		}},
	)

	checks := make([]buildinfo.Check, 0, len(steps))
	for i, s := range steps {
		fmt.Printf("[%d/%d] %s ... ", i+1, len(steps)+1, s.name)
		start := time.Now()
		err := s.run()
		elapsed := time.Since(start)
		if err != nil {
			var skip skipStep
			if errors.As(err, &skip) {
				fmt.Printf("跳过（%s；%s）\n", skip.reason, formatDuration(elapsed))
				checks = append(checks, buildinfo.Check{
					Name:       s.name,
					DurationMS: elapsed.Milliseconds(),
					Skipped:    true,
					SkipReason: skip.reason,
				})
				continue
			}
			fmt.Printf("失败（%s）\n", formatDuration(elapsed))
			return fmt.Errorf("%s：%w", s.name, err)
		}
		fmt.Printf("ok（%s）\n", formatDuration(elapsed))
		checks = append(checks, buildinfo.Check{Name: s.name, DurationMS: elapsed.Milliseconds()})
	}

	// The manifest is the last step: it hashes the binaries the previous steps
	// just built and reads the model connection the run would use.
	fmt.Printf("[%d/%d] 生成构建清单 %s ... ", len(steps)+1, len(steps)+1, cfg.manifestOut)
	manifestStart := time.Now()
	gateInfo := &buildinfo.GateInfo{
		StartedAt: started.UTC().Format(time.RFC3339),
		Skips:     buildinfo.Skips{Web: cfg.skipWeb, Harness: cfg.skipHarness},
		Checks:    checks,
		Embedded:  embedded,
	}
	out := absFrom(root, cfg.manifestOut)
	manifest, err := buildinfo.Collect(context.Background(), buildinfo.Options{
		Root:          root,
		HarnessDir:    harness.Dir,
		BinDir:        cfg.binDir,
		HarnessConfig: cfg.harnessConfig,
		ProbeModel:    probe,
		ProbeTimeout:  cfg.modelProbeWait,
		AOPProto:      aopProto,
		Gate:          gateInfo,
	})
	if err != nil {
		fmt.Printf("失败（%s）\n", formatDuration(time.Since(manifestStart)))
		return fmt.Errorf("生成构建清单：%w", err)
	}
	if err := buildinfo.Write(out, manifest); err != nil {
		fmt.Printf("失败（%s）\n", formatDuration(time.Since(manifestStart)))
		return fmt.Errorf("写入构建清单：%w", err)
	}
	fmt.Printf("ok（%s）\n", formatDuration(time.Since(manifestStart)))

	fmt.Printf("\ntw-gate: 全部检查通过，用时 %s\n\n%s", formatDuration(time.Since(started)),
		buildinfo.Describe(manifest, buildinfo.ManifestRelPath(root, out)))
	return nil
}

// writeManifestOnly implements -manifest-only.
func writeManifestOnly(cfg config, root string) error {
	harness := buildinfo.ResolveHarnessSource(root, cfg.harnessDir)
	out := absFrom(root, cfg.manifestOut)
	probe := cfg.modelProbe && !cfg.noModelProbe
	manifest, err := buildinfo.Collect(context.Background(), buildinfo.Options{
		Root:          root,
		HarnessDir:    harness.Dir,
		BinDir:        cfg.binDir,
		HarnessConfig: cfg.harnessConfig,
		ProbeModel:    probe,
		ProbeTimeout:  cfg.modelProbeWait,
	})
	if err != nil {
		return fmt.Errorf("生成构建清单：%w", err)
	}
	if err := buildinfo.Write(out, manifest); err != nil {
		return fmt.Errorf("写入构建清单：%w", err)
	}
	fmt.Printf("\n%s", buildinfo.Describe(manifest, buildinfo.ManifestRelPath(root, out)))
	return nil
}

// preflight fails early with an actionable message when a prerequisite is
// missing. It never skips a check: a missing prerequisite is an error.
func preflight(cfg config, root string) error {
	if _, err := exec.LookPath("go"); err != nil {
		return errors.New("找不到 go：需要 Go 1.26+（Windows: winget install GoLang.Go 或 https://go.dev/dl/；Debian/Ubuntu: sudo apt-get install -y golang-go 或官方 tarball），装好后重开终端")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("找不到 git：清单需要 git 记录源码身份（Windows 用 Git for Windows，Linux 用发行版 git）")
	}
	if err := requireVersionCommand("node", "找不到 node：前端检查与 scripts/check-docs.mjs 需要 Node.js 24+（Windows: 官方安装包或 winget install OpenJS.NodeJS.LTS；Debian/Ubuntu: 发行版包或官方 Node 24 tarball 解压后把 bin 加入 PATH）"); err != nil {
		return err
	}
	if !cfg.skipWeb {
		if _, err := os.Stat(filepath.Join(root, "web", "node_modules")); err != nil {
			return errors.New("web/node_modules 不存在：先运行 pnpm install（Windows 与 Linux 各自安装本平台的依赖）")
		}
		if err := requireVersionCommand("pnpm", "找不到 pnpm：先装 Node.js 24+，再 npm i -g pnpm（或 corepack enable）；Windows 与 Linux 安装方式相同"); err != nil {
			return err
		}
	}
	return nil
}

// requireVersionCommand checks that a tool not only exists on PATH but actually
// runs. On WSL a Windows shim can be first on PATH and still fail with its own
// "node not found"; resolving that here gives a clear message instead of a
// confusing failure inside a later step.
func requireVersionCommand(name, missingHint string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return errors.New(missingHint)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s 不可用（%s）：%s\n%s", name, path, strings.TrimSpace(string(out)), missingHint)
	}
	return nil
}

// buildHarness builds the Hub and scan binaries with the same tags as
// scripts/build-harness.*: forceposix emptytemplates noembed osusergo sqlite
// (+full for the Hub, +netgo outside Windows). Keeping the tags here, next to
// the gate, is what makes the rebuild part of the checked build.
func buildHarness(srcDir, binDir, goos, exe string) error {
	tags := []string{"forceposix", "emptytemplates", "noembed", "osusergo", "sqlite"}
	if goos != "windows" {
		// Windows' native resolver does not require CGO; forcing netgo would
		// bypass its DNS cache/NRPT.
		tags = append(tags, "netgo")
	}
	env := []string{}
	if strings.TrimSpace(os.Getenv("GOPROXY")) == "" {
		env = append(env, "GOPROXY="+goproxyDefault)
	}
	hub := filepath.Join(binDir, "cyber-harness"+exe)
	scan := filepath.Join(binDir, "cyber-scan"+exe)
	if err := runCommand(srcDir, env, "go", "build", "-tags", strings.Join(append(append([]string{}, tags...), "full"), " "), "-o", hub, "./cmd/cyber-harness"); err != nil {
		return fmt.Errorf("构建 cyber-harness：%w", err)
	}
	if err := runCommand(srcDir, env, "go", "build", "-tags", strings.Join(tags, " "), "-o", scan, "./cmd/cyber-scan"); err != nil {
		return fmt.Errorf("构建 cyber-scan：%w", err)
	}
	return nil
}

// runCommand runs one child with inherited stdio so its output streams into the
// gate log (and any CI-like wrapper) as it happens.
func runCommand(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if err := cmd.Run(); err != nil {
		if cmd.ProcessState != nil {
			return fmt.Errorf("%s 退出码 %d：%w", strings.Join(append([]string{name}, args...), " "), cmd.ProcessState.ExitCode(), err)
		}
		return err
	}
	return nil
}

// repoRoot resolves the checkout root. The scripts cd here first; a manual
// `go run ./cmd/tw-gate` from a subdirectory is still tolerated through git.
func repoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
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
	return "", errors.New("无法定位 tianwu 仓库根：请在仓库根目录运行（scripts/gate.ps1 与 scripts/gate.sh 会自动定位）")
}

func absFrom(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
