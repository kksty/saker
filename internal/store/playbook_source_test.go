package store

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

func mustVerifiedSource(t *testing.T, s *Store, title string) model.Fact {
	t.Helper()
	ev, err := s.AddEvidence(model.Evidence{IntentID: "in_1", URI: "in_1/" + title + ".txt", Name: title + ".txt"})
	if err != nil {
		t.Fatal(err)
	}
	return verifiedSupport(t, s, ev.ID, title)
}

// A confirmation binds the claim versions it accepted. A later correction keeps
// the original receipt and its history but makes the label stale; re-confirming
// the new version is a new explicit write and appends a second receipt.
func TestPlaybookConfirmationBindsSourceClaimVersions(t *testing.T) {
	s := open(t)
	src := mustVerifiedSource(t, s, "来源事实")
	p, changed, err := s.UpsertPlaybook(model.Playbook{
		Title: "复用打法", When: "当条件 A", Origin: model.PlaybookFromExecutor,
		FromFactIDs: []string{src.ID}, SourceRunID: "run_1",
	})
	if err != nil || !changed {
		t.Fatalf("confirm: changed=%v err=%v", changed, err)
	}
	if p.FromFactClaims[src.ID] != src.ClaimVersion {
		t.Fatalf("claim binding = %v, want v%d", p.FromFactClaims, src.ClaimVersion)
	}
	if len(p.Confirmations) != 1 || p.Confirmations[0].FromFactClaims[src.ID] != src.ClaimVersion {
		t.Fatalf("confirmation receipt = %+v", p.Confirmations)
	}
	if !p.Confirmed(model.FactIndex(s.Snapshot().Facts)) {
		t.Fatal("a verified bound source did not confirm the entry")
	}

	// Correct the assertion. The old entry is kept exactly as written; only the
	// read-side label says its basis changed.
	corrected, err := s.UpdateFact(src.ID, func(f *model.Fact) { f.Summary = "更正后的断言" })
	if err != nil {
		t.Fatal(err)
	}
	if corrected.ClaimVersion == src.ClaimVersion {
		t.Fatal("fixture did not bump the claim version")
	}
	stored, _ := s.PlaybookByID(p.ID)
	facts := model.FactIndex(s.Snapshot().Facts)
	if stored.Confirmed(facts) {
		t.Fatal("a corrected source still confirmed the old entry")
	}
	problem := stored.SourceProblem(facts)
	if !strings.Contains(problem, "已更新") || !strings.Contains(problem, src.ID) {
		t.Fatalf("source change not named: %q", problem)
	}
	if stored.FromFactClaims[src.ID] != src.ClaimVersion || len(stored.Confirmations) != 1 {
		t.Fatalf("the old receipt was rewritten: %+v", stored)
	}
	if annotated := stored.WithSourceState(facts); !annotated.SourceChanged || annotated.SourceChange == "" {
		t.Fatalf("source change not exposed for reading: %+v", annotated)
	}

	// A new review of v2 alone is not a new confirmation.
	if _, err := s.ReviewFact(model.Review{FactID: src.ID, Verdict: "pass", VerifierRun: "run_2"}); err != nil {
		t.Fatal(err)
	}
	still, _ := s.PlaybookByID(p.ID)
	if still.Confirmed(model.FactIndex(s.Snapshot().Facts)) {
		t.Fatal("a passing review of the source alone restored the old confirmation")
	}

	// The executor explicitly citing the v2 source is the new receipt.
	again, changed, err := s.UpsertPlaybook(model.Playbook{
		Title: "复用打法", When: "当条件 A", Origin: model.PlaybookFromExecutor,
		FromFactIDs: []string{src.ID}, SourceRunID: "run_3",
	})
	if err != nil || !changed {
		t.Fatalf("re-confirm: changed=%v err=%v", changed, err)
	}
	if again.ID != p.ID {
		t.Fatalf("re-confirmation created a second row: %s vs %s", again.ID, p.ID)
	}
	if again.FromFactClaims[src.ID] != corrected.ClaimVersion || len(again.Confirmations) != 2 {
		t.Fatalf("new confirmation = %+v", again)
	}
	if !again.Confirmed(model.FactIndex(s.Snapshot().Facts)) {
		t.Fatal("the new explicit confirmation did not restore the label")
	}
}

// The same title from two independent sources is two entries with their own
// receipts; a later write continues the matching source, not whichever row the
// title happened to find first.
func TestSameTitlePlaybooksKeepIndependentSourceHistories(t *testing.T) {
	s := open(t)
	first := mustVerifiedSource(t, s, "来源一")
	second := mustVerifiedSource(t, s, "来源二")
	a, _, err := s.UpsertPlaybook(model.Playbook{
		Title: "同名打法", When: "条件一", Origin: model.PlaybookFromExecutor,
		FromFactIDs: []string{first.ID}, SourceRunID: "run_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.UpsertPlaybook(model.Playbook{
		Title: "同名打法", When: "条件二", Origin: model.PlaybookFromExecutor,
		FromFactIDs: []string{second.ID}, SourceRunID: "run_b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("two independent sources with one title were merged")
	}
	rows := s.Playbooks()
	var same int
	for _, row := range rows {
		if row.Title != "同名打法" {
			continue
		}
		same++
		if row.ID == a.ID && (len(row.FromFactIDs) != 1 || row.FromFactIDs[0] != first.ID) {
			t.Fatalf("first row lost its source: %+v", row)
		}
		if row.ID == b.ID && (len(row.FromFactIDs) != 1 || row.FromFactIDs[0] != second.ID) {
			t.Fatalf("second row lost its source: %+v", row)
		}
	}
	if same != 2 {
		t.Fatalf("same-title rows = %d, want 2", same)
	}

	// A write citing the first source updates the first entry only.
	continued, _, err := s.UpsertPlaybook(model.Playbook{
		Title: "同名打法", When: "条件一修订", Origin: model.PlaybookFromExecutor,
		FromFactIDs: []string{first.ID}, SourceRunID: "run_a2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if continued.ID != a.ID {
		t.Fatalf("continuation went to %s, want %s", continued.ID, a.ID)
	}
	if other, _ := s.PlaybookByID(b.ID); len(other.FromFactIDs) != 1 || other.FromFactIDs[0] != second.ID {
		t.Fatalf("the independent row was changed: %+v", other)
	}
}
