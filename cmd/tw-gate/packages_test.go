package main

// The gate compiles ./cmd/... ./internal/... rather than ./..., because the
// default data directory lives inside the module: a pentest engagement stores
// the target's own source files under data/projects/<id>/cases/<case>/raw/, and
// Go reads every directory under the module as a package. "./..." therefore
// fails on any machine that has actually run a project — the gate would go red
// for being used, and the only workaround would be deleting the data.
//
// These tests pin both halves: the pattern set ignores run data, and it still
// covers every package this repository owns.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testRepoRoot = "../.."

// TestGatePatternsIgnoreRunData reproduces the failure mode in a throwaway
// module: a data directory holding the target's own files breaks "./..." while
// the scoped patterns still build.
func TestGatePatternsIgnoreRunData(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/engagement\n\ngo 1.26.0\n")
	write("cmd/tool/main.go", "package main\n\nfunc main() {}\n")
	write("internal/core/core.go", "package core\n")
	// Exactly what a run leaves behind: captured target source that is not a
	// package of this module (a 404 body saved as .go, two packages in one dir).
	write("data/projects/p/cases/in_x/raw/notfound.go", "404 page not found\n")
	write("data/projects/p/cases/in_x/raw/a.go", "package v1\n")
	write("data/projects/p/cases/in_x/raw/session.go", "package middleware\n")

	if err := goCmd(root, "list", "./..."); err == nil {
		t.Fatal("./... accepted the captured target source: the fixture no longer reproduces the pollution")
	}
	if err := goCmd(root, append([]string{"list"}, goPackages...)...); err != nil {
		t.Fatalf("scoped patterns %v failed on a module with run data: %v", goPackages, err)
	}
}

// TestGoPackagesCoverTheModule keeps the scoped set honest. If a package moves
// out of cmd/ or internal/, this fails instead of silently dropping it from
// build/vet/test — which is the only way the narrower patterns could lose
// coverage relative to "./...".
func TestGoPackagesCoverTheModule(t *testing.T) {
	skip := map[string]bool{".git": true, ".build": true, "node_modules": true, "data": true, "tmp": true, "bin": true, "testdata": true}
	var dirs []string
	err := filepath.WalkDir(testRepoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if skip[name] || (strings.HasPrefix(name, ".") && path != testRepoRoot) {
			return filepath.SkipDir
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				rel, err := filepath.Rel(testRepoRoot, path)
				if err != nil {
					return err
				}
				dirs = append(dirs, filepath.ToSlash(rel))
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("found no Go packages: the walk is wrong, not the repo")
	}
	for _, dir := range dirs {
		if dir == "cmd" || strings.HasPrefix(dir, "cmd/") || dir == "internal" || strings.HasPrefix(dir, "internal/") {
			continue
		}
		t.Errorf("package %q is outside %v: add it to goPackages (or move it under cmd/ or internal/)", dir, goPackages)
	}
}

func goCmd(dir string, args ...string) error {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &goError{args: args, out: string(out), err: err}
	}
	return nil
}

type goError struct {
	args []string
	out  string
	err  error
}

func (e *goError) Error() string {
	return "go " + strings.Join(e.args, " ") + ": " + e.err.Error() + "\n" + e.out
}
