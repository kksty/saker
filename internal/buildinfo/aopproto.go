package buildinfo

// Vendored AOP generated-code drift guard.
//
// saker does not own the AOP wire protocol: the generated files under
// internal/aop/{pb,pty,traffic,tool,command,operation,guardrail} are copies of
// the cyber-harness generated files, with only the import paths (and one
// package clause) rewritten for the internal/aop layout. A silent difference
// between the copy and its upstream would either fail to compile or, worse,
// compile into a different wire format, so the gate compares every pair before
// building and the build manifest records the fingerprint that was compared.
//
// The comparison is on a normalized form, not on raw bytes, because the two
// repositories legitimately differ in generated-file noise:
//
//   - the "// Code generated ... versions: protoc-gen-go vX / protoc vY" header
//     lines move whenever either checkout is regenerated with a different
//     protoc toolchain;
//   - the package clause is renamed for the vendored layout (types -> command);
//   - imports point at github.com/kksty/saker/internal/aop/... instead of
//     github.com/chainreactors/cyber/aop/...;
//   - saker occasionally adds a local comment explaining why a file is
//     vendored.
//
// Comments (including those generator header version lines) and blank lines are
// therefore dropped, the package clause is compared name-independently, and the
// documented import rewrites are folded back to the harness form before the
// SHA-256 is taken. Everything else — message descriptors, field numbers,
// enum values, raw descriptor bytes — is compared, which is exactly the wire
// contract. A missing harness checkout is a recorded skip, never a failure; the
// gate already has its own rule for a harness source it needs to build.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// AOPProtoStatus* are the three states of one check run.
const (
	// AOPProtoStatusOK: every vendored file equals its normalized harness source.
	AOPProtoStatusOK = "ok"
	// AOPProtoStatusMismatch: at least one pair differs (the gate fails).
	AOPProtoStatusMismatch = "mismatch"
	// AOPProtoStatusSkipped: there is no harness checkout to compare against.
	// The manifest still records the vendored fingerprint; the gate records a
	// skip and does not fail.
	AOPProtoStatusSkipped = "skipped"
)

// AOPProtoRewrite is one import-path rewrite between the harness generated file
// and the vendored copy. When one harness path is a prefix of another, the
// longer path must be listed first so the base rewrite cannot consume it.
type AOPProtoRewrite struct {
	Harness  string
	Vendored string
}

const (
	harnessAOPProto      = "github.com/chainreactors/cyber/aop"
	harnessAOPOperation  = harnessAOPProto + "/operation"
	vendoredAOPProto     = "github.com/kksty/saker/internal/aop/pb"
	vendoredAOPOperation = "github.com/kksty/saker/internal/aop/operation"
)

// aopProtoImportRewrites is shared by every vendored file: the generated code
// imports the sibling protocol packages, and saker's copies must resolve them
// inside internal/aop instead of the harness module.
var aopProtoImportRewrites = []AOPProtoRewrite{
	{Harness: harnessAOPOperation, Vendored: vendoredAOPOperation},
	{Harness: harnessAOPProto, Vendored: vendoredAOPProto},
}

// AOPProtoFile is one vendored generated file and the harness file it is copied
// from. Paths are slash-separated and relative to the respective repository
// root.
type AOPProtoFile struct {
	// Name is the logical protocol name used in messages (never in paths).
	Name string
	// HarnessPath is the generated file in the cyber-harness checkout.
	HarnessPath string
	// VendoredPath is the copy in the saker checkout.
	VendoredPath string
	// VendoredPackage renames the generated package clause when vendoring moved
	// a file into a differently named package (core/types -> aop/command).
	VendoredPackage string
	// Rewrites are the import-path rewrites for this file.
	Rewrites []AOPProtoRewrite
}

// AOPProtoFiles is the complete vendored AOP generated-file set. Keep it in sync
// with the directories listed in CONTRIBUTING.md and internal/aop/pb/README.md.
var AOPProtoFiles = []AOPProtoFile{
	{Name: "aop/chat", HarnessPath: "aop/chat.pb.go", VendoredPath: "internal/aop/pb/chat.pb.go"},
	{Name: "aop/content", HarnessPath: "aop/content.pb.go", VendoredPath: "internal/aop/pb/content.pb.go"},
	{Name: "aop/envelope", HarnessPath: "aop/envelope.pb.go", VendoredPath: "internal/aop/pb/envelope.pb.go"},
	{Name: "aop/event", HarnessPath: "aop/event.pb.go", VendoredPath: "internal/aop/pb/event.pb.go"},
	{Name: "aop/protocol", HarnessPath: "aop/protocol.pb.go", VendoredPath: "internal/aop/pb/protocol.pb.go"},
	{Name: "aop/value", HarnessPath: "aop/value.pb.go", VendoredPath: "internal/aop/pb/value.pb.go"},
	{Name: "aop/operation", HarnessPath: "aop/operation/protocol.pb.go", VendoredPath: "internal/aop/operation/protocol.pb.go"},
	{Name: "aop/pty", HarnessPath: "aop/pty/protocol.pb.go", VendoredPath: "internal/aop/pty/protocol.pb.go"},
	{Name: "aop/tool", HarnessPath: "aop/tool/protocol.pb.go", VendoredPath: "internal/aop/tool/protocol.pb.go", Rewrites: aopProtoImportRewrites},
	{Name: "aop/traffic", HarnessPath: "aop/traffic/protocol.pb.go", VendoredPath: "internal/aop/traffic/protocol.pb.go", Rewrites: aopProtoImportRewrites},
	// The command protocol is generated into core/types upstream but vendored
	// under aop/command so the console can import it without the whole harness
	// type package.
	{Name: "types/command", HarnessPath: "core/types/command.pb.go", VendoredPath: "internal/aop/command/command.pb.go", VendoredPackage: "command", Rewrites: aopProtoImportRewrites},
	{Name: "exts/guardrail", HarnessPath: "exts/guardrail/guardrail.pb.go", VendoredPath: "internal/aop/guardrail/guardrail.pb.go", Rewrites: aopProtoImportRewrites},
}

// AOPProtoMismatch is one file pair that differed. Line numbers point at the
// first normalized code line that differed in each file (0 when one side ended
// before the other).
type AOPProtoMismatch struct {
	Path        string `json:"path"`
	HarnessPath string `json:"harness_path"`
	SakerLine   int    `json:"saker_line,omitempty"`
	HarnessLine int    `json:"harness_line,omitempty"`
	Detail      string `json:"detail"`
}

// AOPProtoFileDigest is the normalized fingerprint of one vendored file, kept
// per file so a mismatch report and the manifest can name the exact copy.
type AOPProtoFileDigest struct {
	Path        string `json:"path"`
	HarnessPath string `json:"harness_path"`
	SHA256      string `json:"sha256"`
	// Matched is only meaningful when a harness source was available; a skipped
	// check naturally has no comparison result.
	Matched bool `json:"matched"`
}

// AOPProtoInfo is the result of one drift check and the value stored in the
// build manifest under aop_proto.
type AOPProtoInfo struct {
	Status string `json:"status"`
	// Reason explains a skipped status (harness source absent).
	Reason *string `json:"reason,omitempty"`
	// HarnessDir is the compared harness checkout, repository-relative when it
	// lives inside the saker root (slash-separated either way).
	HarnessDir string `json:"harness_dir,omitempty"`
	// HarnessCommit is the harness commit when the checkout is a git tree; a
	// source tarball legitimately has none.
	HarnessCommit *string `json:"harness_commit,omitempty"`
	// SHA256 is the aggregate normalized fingerprint of the vendored files. It
	// is independent of whether a harness source was available, so a manifest
	// still identifies the bytes that were built.
	SHA256 string `json:"sha256"`
	// Files is one entry per vendored file, in AOPProtoFiles order.
	Files []AOPProtoFileDigest `json:"files"`
	// Mismatches is non-empty exactly when Status is AOPProtoStatusMismatch.
	Mismatches []AOPProtoMismatch `json:"mismatches,omitempty"`
}

// canonicalAOPProtoLine is one normalized, comparable code line plus its
// 1-based line number in the original file (for diagnostics).
type canonicalAOPProtoLine struct {
	text string
	line int
}

// canonicalAOPProtoLines reduces a generated file to the lines that carry the
// wire contract. Comments and blank lines are dropped: the generator header
// (including its protoc version lines), proto doc comments and saker's local
// notes are all generated-file noise, while the descriptors that define the
// wire format live in code. Import paths are folded back to the harness form and
// the package clause is compared name-independently. CRLF is normalized first so
// a Windows checkout and a Linux checkout compare equal.
func canonicalAOPProtoLines(content []byte, file AOPProtoFile) []canonicalAOPProtoLine {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	out := make([]canonicalAOPProtoLine, 0, len(lines))
	for i, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		for _, rewrite := range file.Rewrites {
			line = strings.ReplaceAll(line, rewrite.Vendored, rewrite.Harness)
		}
		// The vendored layout renames at most the package clause; the message
		// and descriptor code that follows is what the check compares.
		if strings.HasPrefix(line, "package ") {
			line = "package <normalized>"
		}
		out = append(out, canonicalAOPProtoLine{text: line, line: i + 1})
	}
	return out
}

// canonicalAOPProtoBytes renders the normalized form used for hashing.
func canonicalAOPProtoBytes(content []byte, file AOPProtoFile) []byte {
	var b strings.Builder
	for _, line := range canonicalAOPProtoLines(content, file) {
		b.WriteString(line.text)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// AOPProtoFileSHA256 is the normalized digest of one generated file. Two files
// that differ only in generator version header, local comments, package clause
// or the documented import rewrites hash the same.
func AOPProtoFileSHA256(content []byte, file AOPProtoFile) string {
	sum := sha256.Sum256(canonicalAOPProtoBytes(content, file))
	return hex.EncodeToString(sum[:])
}

// compareAOPProtoFiles returns the first normalized difference, or nil when the
// vendored copy matches the harness source.
func compareAOPProtoFiles(vendored, harness []byte, file AOPProtoFile) *AOPProtoMismatch {
	vendoredLines := canonicalAOPProtoLines(vendored, file)
	harnessLines := canonicalAOPProtoLines(harness, file)
	mismatch := &AOPProtoMismatch{Path: file.VendoredPath, HarnessPath: file.HarnessPath}
	limit := len(vendoredLines)
	if len(harnessLines) < limit {
		limit = len(harnessLines)
	}
	for i := 0; i < limit; i++ {
		if vendoredLines[i].text == harnessLines[i].text {
			continue
		}
		mismatch.SakerLine = vendoredLines[i].line
		mismatch.HarnessLine = harnessLines[i].line
		mismatch.Detail = fmt.Sprintf("saker 第 %d 行 %s 与 harness 第 %d 行 %s 不同",
			vendoredLines[i].line, clipText(vendoredLines[i].text, 160),
			harnessLines[i].line, clipText(harnessLines[i].text, 160))
		return mismatch
	}
	switch {
	case len(vendoredLines) > len(harnessLines):
		mismatch.SakerLine = vendoredLines[limit].line
		mismatch.Detail = fmt.Sprintf("saker 从第 %d 行起多出 %d 行规范化代码",
			vendoredLines[limit].line, len(vendoredLines)-len(harnessLines))
	case len(harnessLines) > len(vendoredLines):
		mismatch.HarnessLine = harnessLines[limit].line
		mismatch.Detail = fmt.Sprintf("saker 缺少 %d 行规范化代码（harness 从第 %d 行起）",
			len(harnessLines)-len(vendoredLines), harnessLines[limit].line)
	default:
		return nil
	}
	return mismatch
}

// AOPProtoVendoredContent turns one harness generated file into the copy saker
// keeps: line endings are normalized, the documented import rewrites are
// applied, and the package clause is renamed when the vendored file lives in a
// differently named package. Nothing else is edited, so the output stays a
// verbatim copy of the generated code.
func AOPProtoVendoredContent(harnessContent []byte, file AOPProtoFile) []byte {
	text := strings.ReplaceAll(string(harnessContent), "\r\n", "\n")
	for _, rewrite := range file.Rewrites {
		text = strings.ReplaceAll(text, rewrite.Harness, rewrite.Vendored)
	}
	if pkg := strings.TrimSpace(file.VendoredPackage); pkg != "" {
		text = renameAOPProtoPackage(text, pkg)
	}
	return []byte(text)
}

// renameAOPProtoPackage rewrites the first package clause of a generated file.
func renameAOPProtoPackage(text, pkg string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "package ") {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + "package " + pkg
		break
	}
	return strings.Join(lines, "\n")
}

// CheckAOPProto compares every vendored AOP generated file with the harness
// source. It never returns an error: a missing harness checkout is
// AOPProtoStatusSkipped and one or more mismatches are reported in the result;
// the caller (the gate) decides whether a mismatch fails the run. The vendor
// fingerprint is computed in every state, because the manifest must describe the
// bytes compiled even when there was nothing to compare them against.
func CheckAOPProto(root, harnessDir string) *AOPProtoInfo {
	info := &AOPProtoInfo{Status: AOPProtoStatusOK}

	fingerprint := sha256.New()
	_, _ = io.WriteString(fingerprint, "saker-aop-proto-v1\n")
	vendored := make(map[string][]byte, len(AOPProtoFiles))
	for _, file := range AOPProtoFiles {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file.VendoredPath)))
		digest := "missing"
		if err == nil {
			digest = AOPProtoFileSHA256(content, file)
			vendored[file.VendoredPath] = content
		}
		fmt.Fprintf(fingerprint, "%s\x00%s\n", file.VendoredPath, digest)
		info.Files = append(info.Files, AOPProtoFileDigest{
			Path:        file.VendoredPath,
			HarnessPath: file.HarnessPath,
			SHA256:      digest,
		})
	}
	info.SHA256 = hex.EncodeToString(fingerprint.Sum(nil))

	resolved := strings.TrimSpace(harnessDir)
	if resolved != "" {
		resolved = absFrom(root, resolved)
		info.HarnessDir = manifestPath(root, resolved)
	}
	if resolved == "" || !isHarnessDir(resolved) {
		info.Status = AOPProtoStatusSkipped
		info.Reason = ptr(AOPProtoSkipReason(root, resolved))
		return info
	}
	if commit, err := harnessCommit(resolved); err == nil && commit != "" {
		info.HarnessCommit = ptr(commit)
	}

	for i, file := range AOPProtoFiles {
		harnessContent, err := os.ReadFile(filepath.Join(resolved, filepath.FromSlash(file.HarnessPath)))
		if err != nil {
			info.Mismatches = append(info.Mismatches, AOPProtoMismatch{
				Path:        file.VendoredPath,
				HarnessPath: file.HarnessPath,
				Detail:      "harness 源缺少该生成文件：" + file.HarnessPath + "（先更新 harness checkout）",
			})
			continue
		}
		vendoredContent, ok := vendored[file.VendoredPath]
		if !ok {
			info.Mismatches = append(info.Mismatches, AOPProtoMismatch{
				Path:        file.VendoredPath,
				HarnessPath: file.HarnessPath,
				Detail:      "saker 缺少 vendored 生成文件：" + file.VendoredPath,
			})
			continue
		}
		if mismatch := compareAOPProtoFiles(vendoredContent, harnessContent, file); mismatch != nil {
			info.Mismatches = append(info.Mismatches, *mismatch)
			continue
		}
		info.Files[i].Matched = true
	}
	if len(info.Mismatches) > 0 {
		info.Status = AOPProtoStatusMismatch
	}
	return info
}

// AOPProtoSkipReason explains a skipped drift check in gate-readable words.
func AOPProtoSkipReason(root, harnessDir string) string {
	where := "未指定 harness 源码目录"
	if strings.TrimSpace(harnessDir) != "" {
		where = "harness 源码缺失（" + manifestPath(root, harnessDir) + "）"
	}
	return where + "：无法逐对比较 vendored AOP 生成文件；可用 -harness-dir、HARNESS_DIR、.build/harness-src 或同父目录 cyber-harness 指向 harness 源"
}

// AOPProtoMismatchMessage renders a mismatch report with the fix command. It is
// the message the gate prints when the drift check fails.
func AOPProtoMismatchMessage(info *AOPProtoInfo) string {
	if info == nil {
		return "AOP 生成代码与 harness 源不一致"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "AOP 生成代码与 harness 源不一致（共 %d 对文件，%d 个不同）", len(AOPProtoFiles), len(info.Mismatches))
	if info.HarnessDir != "" {
		fmt.Fprintf(&b, "；harness 源 %s", info.HarnessDir)
		if info.HarnessCommit != nil {
			fmt.Fprintf(&b, "@%s", *info.HarnessCommit)
		}
	}
	b.WriteString("：\n")
	const maxReported = 10
	for i, mismatch := range info.Mismatches {
		if i == maxReported {
			fmt.Fprintf(&b, "  …… 另有 %d 个文件不一致\n", len(info.Mismatches)-maxReported)
			break
		}
		fmt.Fprintf(&b, "  %s ≠ %s：%s\n", mismatch.Path, mismatch.HarnessPath, mismatch.Detail)
	}
	b.WriteString("修复：从 harness 源重新同步 vendored 生成文件（只复制上游生成文件，不改语义）：\n")
	b.WriteString("  Windows:     pwsh -File scripts/sync-aop-protos.ps1\n")
	b.WriteString("  Linux/macOS: ./scripts/sync-aop-protos.sh\n")
	b.WriteString("不要手改 vendored 的 .pb.go；升级流程见 CONTRIBUTING.md。")
	return b.String()
}

// AOPProtoSummary is one terminal line describing a check result.
func AOPProtoSummary(info *AOPProtoInfo) string {
	if info == nil {
		return "AOP proto：未检查"
	}
	switch info.Status {
	case AOPProtoStatusSkipped:
		return fmt.Sprintf("AOP proto：跳过（%s）", dashPtr(info.Reason))
	case AOPProtoStatusMismatch:
		return fmt.Sprintf("AOP proto：%d/%d 个文件与 harness 源不一致", len(info.Mismatches), len(info.Files))
	default:
		return fmt.Sprintf("AOP proto：%d 个文件一致，fingerprint=%s%s", len(info.Files), info.SHA256, harnessCommitSuffix(info))
	}
}

// harnessCommitSuffix renders "（harness <dir>@<commit>）" for summaries.
func harnessCommitSuffix(info *AOPProtoInfo) string {
	if info.HarnessDir == "" {
		return ""
	}
	commit := ""
	if info.HarnessCommit != nil {
		commit = "@" + *info.HarnessCommit
	}
	return "（harness " + info.HarnessDir + commit + "）"
}

// AOPProtoSyncFile is one copy performed by SyncAOPProto.
type AOPProtoSyncFile struct {
	Path        string `json:"path"`
	HarnessPath string `json:"harness_path"`
	Written     bool   `json:"written"`
}

// AOPProtoSyncResult reports what SyncAOPProto did.
type AOPProtoSyncResult struct {
	HarnessDir    string             `json:"harness_dir"`
	HarnessCommit *string            `json:"harness_commit,omitempty"`
	DryRun        bool               `json:"dry_run"`
	Files         []AOPProtoSyncFile `json:"files"`
}

// SyncAOPProto copies the generated AOP files from the harness checkout into
// saker, applying only the documented import/package rewrites. It is the
// upgrade path behind scripts/sync-aop-protos.*: generated code is never edited
// by hand, it is replaced by the upstream copy. dryRun reports what would be
// written without touching the tree.
func SyncAOPProto(root, harnessDir string, dryRun bool) (*AOPProtoSyncResult, error) {
	resolved := strings.TrimSpace(harnessDir)
	if resolved == "" {
		return nil, fmt.Errorf("缺少 harness 源码目录：%s", HarnessCloneHint(root))
	}
	resolved = absFrom(root, resolved)
	if !isHarnessDir(resolved) {
		return nil, fmt.Errorf("找不到 harness 源码（%s）：%s", manifestPath(root, resolved), HarnessCloneHint(root))
	}
	result := &AOPProtoSyncResult{HarnessDir: manifestPath(root, resolved), DryRun: dryRun}
	if commit, err := harnessCommit(resolved); err == nil && commit != "" {
		result.HarnessCommit = ptr(commit)
	}
	for _, file := range AOPProtoFiles {
		content, err := os.ReadFile(filepath.Join(resolved, filepath.FromSlash(file.HarnessPath)))
		if err != nil {
			return nil, fmt.Errorf("harness 源缺少生成文件 %s（先更新 harness checkout）：%w", file.HarnessPath, err)
		}
		vendored := AOPProtoVendoredContent(content, file)
		dst := filepath.Join(root, filepath.FromSlash(file.VendoredPath))
		existing, readErr := os.ReadFile(dst)
		written := readErr != nil || !bytes.Equal(existing, vendored)
		if written && !dryRun {
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return nil, fmt.Errorf("创建目录 %s：%w", filepath.Dir(dst), err)
			}
			if err := os.WriteFile(dst, vendored, 0o644); err != nil {
				return nil, fmt.Errorf("写入 %s：%w", file.VendoredPath, err)
			}
		}
		result.Files = append(result.Files, AOPProtoSyncFile{
			Path:        file.VendoredPath,
			HarnessPath: file.HarnessPath,
			Written:     written,
		})
	}
	return result, nil
}

// harnessCommit returns the checked-out commit of a secondary checkout; "" for
// a source tree that is not a git checkout (a release tarball is legitimate).
func harnessCommit(dir string) (string, error) {
	out, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(out)
	if commit == "" {
		return "", errors.New("空 commit")
	}
	return commit, nil
}
