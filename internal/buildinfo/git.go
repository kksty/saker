package buildinfo

// Git plumbing for the manifest. Every working-tree query passes
// -c core.fileMode=false: on a /mnt/c checkout (WSL over drvfs) the executable
// bit is emulated, and without the override git reports the whole tree as
// modified — which would make the manifest's dirty flag and patch digest
// meaningless on Linux. The manifest must describe the same checkout the same
// way on both platforms.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// gitOutput runs git in dir and returns stdout. A failure includes stderr so the
// gate can print an actionable message instead of "exit status 128".
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

// gitStat runs a working-tree query with filemode ignored.
func gitStat(dir string, args ...string) (string, error) {
	return gitOutput(dir, append([]string{"-c", "core.fileMode=false"}, args...)...)
}

// splitLines splits command output into non-empty trimmed lines.
func splitLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// hasGit reports whether a git executable is available.
func hasGit() error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("找不到 git：清单需要 git 才能记录源码身份（Windows 用 Git for Windows，Linux 用发行版 git）")
	}
	return nil
}

// collectSource records the saker checkout itself. This is the one piece the
// manifest cannot do without: without a commit there is nothing to trace back.
func collectSource(root string) (SourceIdentity, error) {
	if err := hasGit(); err != nil {
		return SourceIdentity{}, err
	}
	commit, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(commit) == "" {
		return SourceIdentity{}, fmt.Errorf("不是 git 仓库（%s）：清单必须记录源码提交，请在仓库根目录运行", root)
	}
	src := SourceIdentity{Root: filepath.ToSlash(root), Commit: strings.TrimSpace(commit)}
	if branch, err := gitOutput(root, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		src.Branch = strings.TrimSpace(branch) // "HEAD" means detached
	}
	if subject, err := gitOutput(root, "log", "-1", "--format=%s"); err == nil {
		src.Subject = strings.TrimSpace(subject)
	}
	status, err := gitStat(root, "status", "--porcelain")
	if err != nil {
		return SourceIdentity{}, err
	}
	src.Status = splitLines(status)
	src.Dirty = len(src.Status) > 0

	sha, untracked, err := collectPatch(root)
	if err != nil {
		return SourceIdentity{}, err
	}
	src.PatchSHA256 = sha
	src.Untracked = untracked

	submodules, subErr := collectSubmodules(root)
	src.Submodules = submodules
	src.SubmoduleError = subErr
	return src, nil
}

// collectPatch hashes everything the working tree carries on top of HEAD: the
// `git diff HEAD` bytes (staged and unstaged alike, with binary changes
// included) plus one digest per untracked, non-ignored file. The result is one
// SHA-256 that changes whenever the tree deviates from the recorded commit, so
// "dirty=true" alone does not leave the reader guessing which bytes were tested.
func collectPatch(root string) (string, []UntrackedFile, error) {
	diff, err := gitStat(root, "diff", "HEAD", "--binary", "--no-ext-diff", "--no-color")
	if err != nil {
		return "", nil, fmt.Errorf("读取工作树补丁失败：%w", err)
	}
	out, err := gitOutput(root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", nil, fmt.Errorf("列出未跟踪文件失败：%w", err)
	}
	paths := make([]string, 0)
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, filepath.ToSlash(p))
		}
	}
	sort.Strings(paths)

	h := sha256.New()
	_, _ = io.WriteString(h, "saker-build-manifest-patch-v1\n")
	_, _ = io.WriteString(h, "diff:\n")
	_, _ = io.WriteString(h, diff)
	_, _ = io.WriteString(h, "\nuntracked:\n")

	untracked := make([]UntrackedFile, 0, len(paths))
	for _, p := range paths {
		size, sum, kind, err := hashFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			return "", nil, fmt.Errorf("摘要未跟踪文件 %s：%w", p, err)
		}
		untracked = append(untracked, UntrackedFile{Path: p, Kind: kind, Size: size, SHA256: sum})
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\n", p, kind, sum, size)
	}
	return hex.EncodeToString(h.Sum(nil)), untracked, nil
}

// collectSubmodules records the pinned and checked-out commit of every gitlink.
// A missing submodule is recorded, not fatal.
func collectSubmodules(root string) ([]Submodule, *string) {
	pinned := map[string]string{}
	if out, err := gitOutput(root, "ls-tree", "HEAD"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			// <mode> <type> <sha>\t<path>
			tab := strings.IndexByte(line, '\t')
			if tab < 0 {
				continue
			}
			fields := strings.Fields(line[:tab])
			if len(fields) < 3 || fields[0] != "160000" {
				continue
			}
			pinned[filepath.ToSlash(line[tab+1:])] = fields[2]
		}
	}
	checkedOut := map[string]Submodule{}
	out, err := gitOutput(root, "submodule", "status", "--recursive")
	if err != nil {
		msg := strings.TrimSpace(err.Error())
		if len(pinned) == 0 {
			return nil, &msg
		}
		// The pinned gitlinks are still useful; keep them with initialized=false.
	} else {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			flag := byte(' ')
			if c := line[0]; c == ' ' || c == '+' || c == '-' || c == 'U' {
				flag = c
				line = line[1:]
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			path := filepath.ToSlash(fields[1])
			checkedOut[path] = Submodule{
				Path:         path,
				Commit:       fields[0],
				PinnedCommit: pinned[path],
				Initialized:  flag != '-',
			}
		}
	}
	paths := map[string]struct{}{}
	for p := range pinned {
		paths[p] = struct{}{}
	}
	for p := range checkedOut {
		paths[p] = struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for p := range paths {
		ordered = append(ordered, p)
	}
	sort.Strings(ordered)
	subs := make([]Submodule, 0, len(ordered))
	for _, p := range ordered {
		if sub, ok := checkedOut[p]; ok {
			if sub.PinnedCommit == "" {
				sub.PinnedCommit = pinned[p]
			}
			subs = append(subs, sub)
			continue
		}
		subs = append(subs, Submodule{Path: p, Commit: pinned[p], PinnedCommit: pinned[p]})
	}
	return subs, nil
}

// gitIdentity is the branch/commit/dirty triple for a secondary checkout (the
// harness). Unlike the source checkout it may legitimately be absent or not be a
// git tree, so errors are values, not failures.
func gitIdentity(dir string) (commit, branch, subject *string, dirty *bool, err error) {
	c, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(c) == "" {
		return nil, nil, nil, nil, fmt.Errorf("%s 不是 git checkout", dir)
	}
	commit = ptr(strings.TrimSpace(c))
	if b, err := gitOutput(dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		branch = ptr(strings.TrimSpace(b))
	}
	if s, err := gitOutput(dir, "log", "-1", "--format=%s"); err == nil {
		subject = ptr(strings.TrimSpace(s))
	}
	status, err := gitStat(dir, "status", "--porcelain")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	d := len(splitLines(status)) > 0
	dirty = &d
	return commit, branch, subject, dirty, nil
}

// hashFile returns a file's size and SHA-256. A symlink is hashed by its target
// string so the manifest describes a checkout without following the link.
//
// Text files are hashed with line endings normalised to LF. A Windows checkout
// (core.autocrlf=true) stores CRLF while `vite build` writes LF, so the embedded
// artifact and a fresh rebuild of the same sources are the same product with two
// different byte streams. Hashing raw bytes made the gate report every asset as
// stale on a checkout that had been restored from git — a phantom failure that
// hid the real signal. Binary content (any NUL byte in the head) is hashed as-is
// and stays byte-exact.
func hashFile(path string) (size int64, sum, kind string, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, "", "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return 0, "", "", err
		}
		h := sha256.Sum256([]byte(target))
		return int64(len(target)), hex.EncodeToString(h[:]), "symlink", nil
	}
	if info.IsDir() {
		return 0, "", "", errors.New("是一个目录，不是文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, "", "", err
	}
	defer f.Close()

	head := make([]byte, 8192)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return 0, "", "", err
	}
	head = head[:n]
	binary := false
	for _, b := range head {
		if b == 0 {
			binary = true
			break
		}
	}
	if binary {
		h := sha256.New()
		h.Write(head)
		rest, err := io.Copy(h, f)
		if err != nil {
			return 0, "", "", err
		}
		return int64(n) + rest, hex.EncodeToString(h.Sum(nil)), "file", nil
	}
	rest, err := io.ReadAll(f)
	if err != nil {
		return 0, "", "", err
	}
	data := append(head, rest...)
	if bytesContainsCRLF(data) {
		data = bytesReplaceCRLF(data)
	}
	h := sha256.Sum256(data)
	return int64(len(data)), hex.EncodeToString(h[:]), "file", nil
}

// bytesContainsCRLF reports whether data has at least one CRLF pair, so the
// common case (a rebuild that is already LF) allocates nothing.
func bytesContainsCRLF(data []byte) bool {
	for i := 1; i < len(data); i++ {
		if data[i] == '\n' && data[i-1] == '\r' {
			return true
		}
	}
	return false
}

// bytesReplaceCRLF returns data with every CRLF pair collapsed to LF.
func bytesReplaceCRLF(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			continue
		}
		out = append(out, data[i])
	}
	return out
}

// GitStatusPaths is the porcelain status of one pathspec of the checkout, with
// filemode ignored. The gate uses it around the frontend rebuild to record the
// embedded-artifact state; the content digest, not this string, decides whether
// the rebuild produced a new difference.
func GitStatusPaths(root, pathspec string) ([]string, error) {
	out, err := gitStat(root, "status", "--porcelain", "--", pathspec)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// ResolveHarnessSource locates the independent cyber-harness checkout. The
// lookup order is explicit everywhere (flag, environment, in-repo scratch dir,
// sibling checkout) so a gate run reports which source it actually built.
type HarnessSource struct {
	Dir    string
	Origin string
	Found  bool
}

// ResolveHarnessSource implements the shared lookup used by cmd/saker-gate,
// cmd/saker-manifest and the scripts.
func ResolveHarnessSource(root, explicit string) HarnessSource {
	if p := strings.TrimSpace(explicit); p != "" {
		dir := absFrom(root, p)
		return HarnessSource{Dir: dir, Origin: "flag -harness-dir", Found: isHarnessDir(dir)}
	}
	if p := strings.TrimSpace(os.Getenv("HARNESS_DIR")); p != "" {
		dir := absFrom(root, p)
		return HarnessSource{Dir: dir, Origin: "environment HARNESS_DIR", Found: isHarnessDir(dir)}
	}
	for _, candidate := range []struct {
		path   string
		origin string
	}{
		{filepath.Join(root, ".build", "harness-src"), "default .build/harness-src"},
		// A sibling checkout is the documented local layout (saker and
		// cyber-harness cloned next to each other). No deeper guessing: a stale
		// unrelated directory must not be picked up as the harness.
		{filepath.Join(root, "..", "cyber-harness"), "sibling ../cyber-harness"},
	} {
		if isHarnessDir(candidate.path) {
			abs, err := filepath.Abs(candidate.path)
			if err != nil {
				abs = candidate.path
			}
			return HarnessSource{Dir: abs, Origin: candidate.origin, Found: true}
		}
	}
	return HarnessSource{Dir: filepath.Join(root, ".build", "harness-src"), Origin: "default .build/harness-src"}
}

func isHarnessDir(dir string) bool {
	if dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil && !info.IsDir()
}

// HarnessCloneHint is the clone/checkout guidance a missing harness source gets.
// It is identical on both platforms: the repository is the same, only the shell
// running the command differs.
func HarnessCloneHint(root string) string {
	return "找不到 harness 源码：本仓库不内置 harness/。先单独 clone kksty/cyber-harness 的 saker/fixes：" +
		"\n  git clone --branch saker/fixes https://github.com/kksty/cyber-harness.git " + filepath.Join(root, ".build", "harness-src") +
		"\n或把已有 checkout 用 -harness-dir（Go 参数）、HARNESS_DIR 环境变量或 .build/harness-src 指过来。"
}

// absFrom resolves p against root unless it is already absolute.
func absFrom(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

// manifestPath renders a path for the manifest: repository-relative with forward
// slashes when inside root, absolute with forward slashes otherwise.
func manifestPath(root, path string) string {
	abs := absFrom(root, path)
	if rel, err := filepath.Rel(root, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(abs)
}

func ptr[T any](v T) *T { return &v }
