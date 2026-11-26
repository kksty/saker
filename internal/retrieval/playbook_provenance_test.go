package retrieval

import (
	"path/filepath"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The read side decides whether a playbook may be called confirmed: the stored
// status is only the writer's declaration. These tests pin that a declared-
// confirmed tactic tracks its sources on every read — downgraded while a source
// is unverified or withdrawn, restored when a review passes, never rewritten.

func playbookSource(t *testing.T, s *store.Store, status string, evidence bool) model.Fact {
	t.Helper()
	f := model.Fact{Kind: "vuln", Title: "来源 " + status}
	if status != "verified" {
		f.Status = status
	}
	if evidence {
		// The cited row must exist: production only ever cites ids that ingest
		// created (ledger/ingest.go:153-158), and the write gate now refuses a pass
		// that names a row nobody preserved. Hard-coding "ev_fixture" here would
		// only test the hole that rule closes.
		ev, err := s.AddEvidence(model.Evidence{URI: "in_1/proof.txt", Name: "proof.txt"})
		if err != nil {
			t.Fatal(err)
		}
		f.EvidenceIDs = []string{ev.ID}
	}
	out, err := s.AddFact(f)
	if err != nil {
		t.Fatal(err)
	}
	if status == "verified" {
		// Verified comes from a bound pass, never from the writer.
		if _, err := s.ReviewFact(model.Review{FactID: out.ID, Verdict: "pass"}); err != nil {
			t.Fatal(err)
		}
		reviewed, ok := s.FactByID(out.ID)
		if !ok {
			t.Fatalf("fact %s vanished after review", out.ID)
		}
		out = reviewed
	}
	return out
}

func declaredPlaybook(t *testing.T, s *store.Store, title string, sources ...string) model.Playbook {
	t.Helper()
	p, _, err := s.UpsertPlaybook(model.Playbook{
		Title: title, When: "适用条件", Doc: "步骤",
		Origin: model.PlaybookFromExecutor, Status: model.PlaybookConfirmed,
		FromFactIDs: sources,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func recalled(t *testing.T, s *store.Store, id string) Experience {
	t.Helper()
	for _, row := range ExperienceCandidates(s.Snapshot(), ExperienceQuery{}) {
		if row.Playbook.ID == id {
			return row
		}
	}
	t.Fatalf("playbook %s was not recalled at all", id)
	return Experience{}
}

// An executor's declaration is recalled even before its source is reviewed: the
// tactic keeps its text, but it is a candidate, not a confirmed entry.
func TestUnverifiedSourceRecallsThePlaybookAsCandidate(t *testing.T) {
	s := ledger(t)
	src := playbookSource(t, s, "unverified", true)
	p := declaredPlaybook(t, s, "游标注入", src.ID)
	if got := recalled(t, s, p.ID); got.Playbook.Status != model.PlaybookCandidate {
		t.Fatalf("unverified source recalled as %q, want candidate", got.Playbook.Status)
	}
}

// The counterpart: a source the verifier proved, with bytes, keeps the entry
// confirmed.
func TestVerifiedSourceKeepsThePlaybookConfirmed(t *testing.T) {
	s := ledger(t)
	src := playbookSource(t, s, "verified", true)
	p := declaredPlaybook(t, s, "游标注入", src.ID)
	if got := recalled(t, s, p.ID); got.Playbook.Status != model.PlaybookConfirmed {
		t.Fatalf("verified source recalled as %q, want confirmed", got.Playbook.Status)
	}
}

// Withdrawal propagates to the tactic through the read, without a migration and
// without rewriting the playbook row.
func TestWithdrawingTheSourceDowngradesTheRecalledPlaybook(t *testing.T) {
	s := ledger(t)
	src := playbookSource(t, s, "verified", true)
	p := declaredPlaybook(t, s, "游标注入", src.ID)
	if got := recalled(t, s, p.ID); got.Playbook.Status != model.PlaybookConfirmed {
		t.Fatalf("fixture source was not confirmed: %q", got.Playbook.Status)
	}
	if _, err := s.WithdrawFact(src.ID, "operator", "", "来源不成立"); err != nil {
		t.Fatal(err)
	}
	if got := recalled(t, s, p.ID); got.Playbook.Status != model.PlaybookCandidate {
		t.Fatalf("a withdrawn source still recalled as %q", got.Playbook.Status)
	}
	stored, ok := s.PlaybookByID(p.ID)
	if !ok || stored.Status != model.PlaybookConfirmed {
		t.Fatalf("the stored declaration was rewritten by a read: %+v", stored)
	}
}

// The other direction: once the source passes review, the same stored row is
// confirmed again. Without this the downgrade would be a permanent freeze.
func TestReviewPassRestoresTheRecalledPlaybook(t *testing.T) {
	s := ledger(t)
	src := playbookSource(t, s, "unverified", true)
	p := declaredPlaybook(t, s, "游标注入", src.ID)
	if got := recalled(t, s, p.ID); got.Playbook.Status != model.PlaybookCandidate {
		t.Fatalf("unreviewed source recalled as %q", got.Playbook.Status)
	}
	if _, err := s.ReviewFact(model.Review{FactID: src.ID, Verdict: "pass", VerifierRun: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if got := recalled(t, s, p.ID); got.Playbook.Status != model.PlaybookConfirmed {
		t.Fatalf("a passed review did not restore the label: %q", got.Playbook.Status)
	}
}

// A declared candidate never becomes confirmed on read, even with perfect
// sources: the commander's nomination is a proposal, not a decision.
func TestCommanderNominationStaysACandidateWithGoodSources(t *testing.T) {
	s := ledger(t)
	src := playbookSource(t, s, "verified", true)
	p, _, err := s.UpsertPlaybook(model.Playbook{
		Title: "ThinkPHP 反序列化", When: "5.1 再试",
		Origin: model.PlaybookFromCommander, Status: model.PlaybookCandidate,
		FromFactIDs: []string{src.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := recalled(t, s, p.ID); got.Playbook.Status != model.PlaybookCandidate {
		t.Fatalf("a nomination was promoted by its sources alone: %q", got.Playbook.Status)
	}
}

// The KB list and its counts use the same derived label as the recall: a
// confirmed declaration with an unqualified source lives under candidate in
// both counts and rows, and filtering by confirmed cannot return it.
func TestPlaybookListCountsAndFilterUseTheDerivedLabel(t *testing.T) {
	s := ledger(t)
	good := playbookSource(t, s, "verified", true)
	bad := playbookSource(t, s, "unverified", true)
	qualified := declaredPlaybook(t, s, "合格打法", good.ID)
	unqualified := declaredPlaybook(t, s, "来源未核验的打法", bad.ID)
	if _, _, err := s.UpsertPlaybook(model.Playbook{
		Title: "提名候选", When: "w", Origin: model.PlaybookFromCommander, Status: model.PlaybookCandidate,
	}); err != nil {
		t.Fatal(err)
	}

	page, err := Playbooks(s, PlaybookQuery{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Counts[model.PlaybookConfirmed] != 1 || page.Counts[model.PlaybookCandidate] != 2 {
		t.Fatalf("counts = %v, want 1 confirmed / 2 candidates", page.Counts)
	}
	if len(page.Playbooks) != 1 || page.Playbooks[0].ID != qualified.ID {
		t.Fatalf("confirmed filter = %+v", page.Playbooks)
	}
	candidates, err := Playbooks(s, PlaybookQuery{Status: "candidate"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates.Playbooks) != 2 {
		t.Fatalf("candidate filter = %+v", candidates.Playbooks)
	}
	for _, p := range candidates.Playbooks {
		if p.ID == unqualified.ID && p.Status != model.PlaybookCandidate {
			t.Fatalf("downgraded row kept its declaration: %+v", p)
		}
	}
	// The qualified entry leads the unfiltered list: the sort uses the derived
	// label too.
	all, err := Playbooks(s, PlaybookQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Playbooks) == 0 || all.Playbooks[0].ID != qualified.ID {
		t.Fatalf("confirmed entry did not lead: %+v", all.Playbooks)
	}
}

// Reading never writes back: after a list read and an archive, the journal still
// holds the writer's declaration and its provenance versions.
func TestReadingDoesNotRewriteTheStoredDeclaration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	src := playbookSource(t, s, "verified", true)
	p := declaredPlaybook(t, s, "读取不写回", src.ID)
	if _, err := Playbooks(s, PlaybookQuery{}); err != nil {
		t.Fatal(err)
	}
	version := p.FromFactVersions[src.ID]
	if version == 0 {
		t.Fatalf("provenance version was not recorded: %+v", p)
	}
	if _, err := s.ArchivePlaybook(p.ID, p.UpdatedAt, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen from disk: only the journal the test wrote is visible.
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, ok := reopened.PlaybookByID(p.ID)
	if !ok {
		t.Fatal("playbook missing after reopen")
	}
	if got.Status != model.PlaybookConfirmed {
		t.Fatalf("an archive write changed the declaration: %+v", got)
	}
	if got.FromFactVersions[src.ID] != version {
		t.Fatalf("provenance versions changed: %v -> %v", version, got.FromFactVersions)
	}
}
