package buildinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun runs one git command in a throwaway repository.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.test",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// initRepo makes a throwaway git repository with one commit. The patch-digest
// tests need real git semantics (staged + unstaged + untracked); everything else
// in the package is pure.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不可用，跳过仓库身份测试")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "tracked.txt")
	gitRun(t, dir, "commit", "-q", "-m", "initial commit")
	return dir
}

func TestCollectSourceRecordsCommitDirtyAndPatchDigest(t *testing.T) {
	dir := initRepo(t)

	clean, err := collectSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if clean.Commit == "" || clean.Subject != "initial commit" {
		t.Fatalf("identity = %+v", clean)
	}
	if clean.Dirty {
		t.Fatalf("fresh repo reported dirty: %v", clean.Status)
	}
	if clean.PatchSHA256 == "" {
		t.Fatal("empty patch digest")
	}
	if len(clean.Submodules) != 0 {
		t.Fatalf("unexpected submodules: %+v", clean.Submodules)
	}

	// Working-tree change only (no commit): dirty and a different digest, and the
	// digest must be deterministic while the tree does not change.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := collectSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.Dirty {
		t.Fatal("modified tree reported clean")
	}
	if dirty.PatchSHA256 == clean.PatchSHA256 {
		t.Fatal("patch digest did not change with the working tree")
	}
	again, err := collectSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.PatchSHA256 != dirty.PatchSHA256 {
		t.Fatal("patch digest is not deterministic for an unchanged tree")
	}
	if again.Commit != clean.Commit {
		t.Fatal("commit changed without a commit")
	}
}

func TestPatchDigestFoldsUntrackedFileContent(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := collectSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Untracked) != 1 || first.Untracked[0].Path != "new.txt" {
		t.Fatalf("untracked = %+v", first.Untracked)
	}
	shaA := first.PatchSHA256

	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := collectSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second.PatchSHA256 == shaA {
		t.Fatal("untracked content change did not change the patch digest")
	}
	if first.Untracked[0].SHA256 == second.Untracked[0].SHA256 {
		t.Fatal("untracked digest did not change")
	}
}

func TestResolveHarnessSourceLookupOrder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HARNESS_DIR", "")

	explicit := filepath.Join(root, "explicit")
	if err := os.MkdirAll(filepath.Join(explicit, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(explicit, "go.mod"), []byte("module example.test/harness\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveHarnessSource(root, explicit); !got.Found || got.Dir != explicit {
		t.Fatalf("explicit source = %+v", got)
	}

	// No explicit flag: the in-repo scratch dir wins.
	scratch := filepath.Join(root, ".build", "harness-src")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "go.mod"), []byte("module example.test/harness\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveHarnessSource(root, ""); !got.Found || got.Dir != scratch || !strings.Contains(got.Origin, ".build/harness-src") {
		t.Fatalf("scratch source = %+v", got)
	}

	// HARNESS_DIR beats the in-repo scratch dir.
	envDir := filepath.Join(root, "env-harness")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "go.mod"), []byte("module example.test/harness\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_DIR", envDir)
	if got := ResolveHarnessSource(root, ""); !got.Found || got.Dir != envDir || !strings.Contains(got.Origin, "HARNESS_DIR") {
		t.Fatalf("env source = %+v", got)
	}

	// Nothing found: the default path is still named so the gate can print it.
	t.Setenv("HARNESS_DIR", "")
	empty := t.TempDir()
	if got := ResolveHarnessSource(empty, ""); got.Found {
		t.Fatalf("unexpected source in empty root: %+v", got)
	}
}

func TestManifestPathIsRelativeInsideRoot(t *testing.T) {
	root := t.TempDir()
	if got := manifestPath(root, filepath.Join(root, "bin", "saker.exe")); got != "bin/saker.exe" {
		t.Fatalf("inside path = %q", got)
	}
	outside := filepath.Join(t.TempDir(), "saker.exe")
	if got := manifestPath(root, outside); !strings.Contains(got, "saker.exe") || strings.Contains(got, "..") {
		t.Fatalf("outside path = %q", got)
	}
}
