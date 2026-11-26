package main

// The AOP drift check and the vendored-code sync share one implementation here
// so scripts/gate.* and scripts/sync-aop-protos.* cannot diverge: the scripts are
// thin entries that forward their arguments to this command. The comparison and
// rewrite rules live in internal/buildinfo (aopproto.go); this file only decides
// what a mismatch or a missing harness checkout means for the process.

import (
	"errors"
	"fmt"

	"github.com/kksty/saker/internal/buildinfo"
)

// skipStep marks an optional check whose prerequisite is absent. The gate
// records it as skipped with the reason instead of failing the run; a failure
// must always mean a check that ran and found a problem.
type skipStep struct{ reason string }

func (s skipStep) Error() string { return s.reason }

// runAOPProtoCheck is the single check behind the gate step and
// -check-aop-protos. A mismatch is an error (the gate fails with the fix
// command); a missing harness checkout is a skipStep (recorded, not a failure).
func runAOPProtoCheck(root string, harness buildinfo.HarnessSource) (*buildinfo.AOPProtoInfo, error) {
	info := buildinfo.CheckAOPProto(root, harness.Dir)
	switch info.Status {
	case buildinfo.AOPProtoStatusSkipped:
		reason := ""
		if info.Reason != nil {
			reason = *info.Reason
		}
		return info, skipStep{reason: reason}
	case buildinfo.AOPProtoStatusMismatch:
		return info, errors.New(buildinfo.AOPProtoMismatchMessage(info))
	}
	return info, nil
}

// runCheckAOPProtos implements -check-aop-protos: run only the drift check.
// Skipping without a harness checkout exits 0, matching the gate step.
func runCheckAOPProtos(cfg config) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	harness := buildinfo.ResolveHarnessSource(root, cfg.harnessDir)
	info, err := runAOPProtoCheck(root, harness)
	fmt.Printf("saker-gate: %s\n", buildinfo.AOPProtoSummary(info))
	var skip skipStep
	if errors.As(err, &skip) {
		fmt.Println("saker-gate: AOP proto 检查跳过：harness 源缺失时与 gate 行为一致（记录 skip，不失败）")
		return nil
	}
	return err
}

// runSyncAOPProtos implements -sync-aop-protos: copy the harness generated
// files into internal/aop with only the documented rewrites, then run the same
// drift check that the gate runs. A dry run reports what would change without
// writing (and does not fail on the differences it would fix).
func runSyncAOPProtos(cfg config) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	harness := buildinfo.ResolveHarnessSource(root, cfg.harnessDir)
	if !harness.Found {
		return errors.New(buildinfo.HarnessCloneHint(root))
	}
	result, err := buildinfo.SyncAOPProto(root, harness.Dir, cfg.aopDryRun)
	if err != nil {
		return err
	}
	mode := "同步"
	if result.DryRun {
		mode = "dry-run（不写盘）"
	}
	fmt.Printf("saker-gate: %s AOP 生成代码 ← %s%s\n", mode, result.HarnessDir, aopCommitSuffix(result.HarnessCommit))
	written := 0
	for _, file := range result.Files {
		if !file.Written {
			continue
		}
		written++
		verb := "写入"
		if result.DryRun {
			verb = "将写入"
		}
		fmt.Printf("  %s %s ← %s\n", verb, file.Path, file.HarnessPath)
	}
	if written == 0 {
		fmt.Printf("  %d 个文件已是最新\n", len(result.Files))
	}

	info, err := runAOPProtoCheck(root, harness)
	fmt.Printf("saker-gate: %s\n", buildinfo.AOPProtoSummary(info))
	if result.DryRun {
		if err != nil {
			fmt.Println("saker-gate: dry-run 未写盘；重新运行不带 -aop-dry-run 即可应用上面的更新")
		}
		return nil
	}
	return err
}

// aopCommitSuffix renders "@<commit>" for a sync log line.
func aopCommitSuffix(commit *string) string {
	if commit == nil || *commit == "" {
		return ""
	}
	return "@" + *commit
}
