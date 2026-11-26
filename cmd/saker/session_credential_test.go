package main

import (
	"testing"

	"github.com/kksty/saker/internal/collab"
)

// sessionCredential issues the commander's per-session identity, the credential
// that opens the project mux (/p/<id>/...). Executors have their own identity
// (participant id pt_<intent>) and reach the same gate when they read the shared
// battlefield; tests that exercise that scope attach it explicitly.
func sessionCredential(t *testing.T, registry *collab.Registry, project string) collab.Identity {
	t.Helper()
	ident, err := registry.Issue(collab.Identity{
		ProjectID: project, ParticipantID: collab.CommanderID, RunID: "r_session",
	})
	if err != nil {
		t.Fatal(err)
	}
	return ident
}
