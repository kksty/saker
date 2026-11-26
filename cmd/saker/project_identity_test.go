package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kksty/saker/internal/api"
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/project"
	"github.com/kksty/saker/internal/vulnlib"
)

type deleteStubExec struct{}

func (deleteStubExec) Name() string { return "delete-stub" }

func (deleteStubExec) Run(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
	return executor.Result{}, nil
}

func (deleteStubExec) CanResume(string, string) bool { return false }

// Deleting a project stops its runtime, so the commander's session credential
// must stop resolving with it. The collab registry is process-wide: leaving the
// token behind would make it a second commander credential for a project id that
// is later re-created (or re-attached after a failed delete).
func TestProjectDeletionRevokesTheCommanderIdentity(t *testing.T) {
	root := t.TempDir()
	mgr, err := project.Open(filepath.Join(root, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Create("revoke", "")
	if err != nil {
		t.Fatal(err)
	}
	vulns, err := vulnlib.Open(filepath.Join(root, "vulns"))
	if err != nil {
		t.Fatal(err)
	}
	registry := collab.NewRegistry(time.Hour)
	srv := api.New(p.Store, deleteStubExec{}, p.Dir, p.CaseDir)
	srv.Project = p
	srv.Collab = registry
	if _, _, err := srv.StartCommander(context.Background(), "## 本轮\n\n## board\n{}\n", false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(p.CaseDir), "commander", ".saker", "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ident collab.Identity
	if err := json.Unmarshal(raw, &ident); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Resolve(ident.Token); !ok {
		t.Fatal("commander identity does not resolve before deletion")
	}

	h := &hub{mgr: mgr, vulns: vulns, kits: map[string]*kit{
		p.ID: {proj: p, srv: srv, cancel: func() {}},
	}}
	rec := httptest.NewRecorder()
	h.deleteProject(rec, httptest.NewRequest(http.MethodDelete, "/api/projects/"+p.ID+"?confirm="+p.ID, nil), p.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := registry.Resolve(ident.Token); ok {
		t.Fatal("commander token survived project deletion")
	}
}
