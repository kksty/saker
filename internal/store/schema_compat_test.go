package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// rewriteLedgerVersion stamps a closed ledger with the schema version an older
// build wrote. The ledger is a private file, so the test edits its meta row the
// same way a previous release left it: no DDL change, only the integer.
func rewriteLedgerVersion(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteURL(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var raw []byte
	if err := db.QueryRow("SELECT payload FROM meta WHERE id=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var meta sqliteMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	meta.Version = version
	out, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE meta SET payload=? WHERE id=1", out); err != nil {
		t.Fatal(err)
	}
}

func ledgerVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteURL(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var raw []byte
	if err := db.QueryRow("SELECT payload FROM meta WHERE id=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var meta sqliteMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	return meta.Version
}

// The pre-proposal layout keeps opening: its rows and DDL are the same, and a
// project that never wrote a proposal has no reason to be locked out of a newer
// build. A write that stays inside the old layout must not rewrite the stamp —
// the stamp says what the file may contain, not who opened it, so a silent
// upgrade would lock an untouched project out of the older build.
func TestLedgerSchema13StaysReadableAndStays13(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CreateIntent(model.Intent{Title: "旧项目里的意图"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rewriteLedgerVersion(t, path, 13)

	s2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("a schema 13 ledger was refused: %v", err)
	}
	if got, ok := s2.GetIntent(in.ID); !ok || got.Title != "旧项目里的意图" {
		t.Fatalf("schema 13 ledger lost its rows: %+v", got)
	}
	if _, err := s2.UpdateIntent(in.ID, func(i *model.Intent) { i.Doc = "写入之后仍然读得回来" }); err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	if v := ledgerVersion(t, path); v != 13 {
		t.Fatalf("an old-layout-only write upgraded the stamp to %d", v)
	}
	s3, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("the written schema 13 ledger no longer opens: %v", err)
	}
	defer func() { _ = s3.Close() }()
	if got, ok := s3.GetIntent(in.ID); !ok || got.Doc != "写入之后仍然读得回来" {
		t.Fatalf("round trip lost the write: %+v", got)
	}
}

// A ledger from a future build is still refused: accepting it would read rows
// this build may not understand, and the refusal is what keeps a downgrade from
// looking like success.
func TestLedgerSchema15IsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rewriteLedgerVersion(t, path, 15)
	if _, err := OpenSQLite(path); err == nil {
		t.Fatal("a schema 15 ledger was accepted")
	} else if !strings.Contains(err.Error(), "unsupported ledger schema 15") {
		t.Fatalf("refusal does not name the schema: %v", err)
	}
}

// A proposal is the one row the older layout cannot place, so the first commit
// that writes one moves the stamp to 14 in the same transaction. Staying at 13
// would make the older build refuse the file for an unreadable row ("unknown
// ledger row proposal") instead of stating the truth ("schema 14"): the one-way
// door belongs at the moment the new capability is used, not at the first open.
//
// A schema 13 ledger that already holds a proposal row can only come from a
// hand-edited stamp; this build reads it, and the read itself must upgrade the
// stamp on the next commit rather than leave the two disagreeing.
func TestLedgerSchema13ProposalWriteBumpsTo14(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rewriteLedgerVersion(t, path, 13)

	s2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("a schema 13 ledger was refused: %v", err)
	}
	if _, _, err := s2.CreateProposal(model.Proposal{
		ID: "pr_1", IdempotencyKey: "key-1", FromParticipantID: "exec_1", Title: "提议",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	if v := ledgerVersion(t, path); v != 14 {
		t.Fatalf("the stamp after the first proposal is %d, want 14", v)
	}
	s3, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("the upgraded ledger does not open: %v", err)
	}
	defer func() { _ = s3.Close() }()
	if got, ok := s3.GetProposal("pr_1"); !ok || got.Title != "提议" {
		t.Fatalf("the proposal did not survive: %+v", got)
	}
}
