package api

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The handoff brief is an ordinary dispatch's fact view, not a second rendering
// rule: an unverified assertion may not become readable through a handoff when
// the project still has reviews on. The id, claim version and status stay in
// every case, and a changed claim is called out instead of printed.
func TestHandoffFactsUseTheSameVisibilityRuleAsDispatch(t *testing.T) {
	fact := model.Fact{
		ID: "f_1", Kind: "vuln", Title: "疑似越权", Summary: "UNVERIFIED-BODY",
		Status: "unverified", ClaimVersion: 3, EvidenceIDs: []string{"e_1"},
	}
	db := store.DB{
		Facts: []model.Fact{fact},
		Evidence: []model.Evidence{
			{ID: "e_1", URI: "cases/x/e_1.txt"},
		},
	}
	h := model.Handoff{
		Facts:             []string{fact.ID},
		FactVersions:      map[string]uint64{fact.ID: 2},
		FactClaimVersions: map[string]uint64{fact.ID: fact.ClaimVersion},
	}

	withReviews := handoffSection(db, model.Intent{ID: "in_1"}, h, handoffAuthorization{}, false)
	if strings.Contains(withReviews, "UNVERIFIED-BODY") {
		t.Fatalf("handoff exposed an unverified body while reviews are on:\n%s", withReviews)
	}
	for _, want := range []string{fact.ID, "断言 v3", "unverified"} {
		if !strings.Contains(withReviews, want) {
			t.Fatalf("handoff lost %q:\n%s", want, withReviews)
		}
	}

	// Review off is the one case where the body may be shown; the status still
	// says unverified, because visibility is not promotion.
	withoutReviews := handoffSection(db, model.Intent{ID: "in_1"}, h, handoffAuthorization{}, true)
	if !strings.Contains(withoutReviews, "UNVERIFIED-BODY") || !strings.Contains(withoutReviews, "unverified") {
		t.Fatalf("handoff with reviews off must show the body but keep the status:\n%s", withoutReviews)
	}

	// A verified fact shows its body in both modes.
	verified := fact
	verified.Status = "verified"
	verifiedDB := store.DB{Facts: []model.Fact{verified}, Evidence: db.Evidence}
	if got := handoffSection(verifiedDB, model.Intent{ID: "in_1"}, h, handoffAuthorization{}, false); !strings.Contains(got, "UNVERIFIED-BODY") {
		t.Fatalf("a verified cited fact lost its body:\n%s", got)
	}

	// The claim moved after the handoff was written: name the versions and tell
	// the reader to re-read by id, never print the old body as if it still held.
	moved := h
	moved.FactClaimVersions = map[string]uint64{fact.ID: 2}
	got := handoffSection(db, model.Intent{ID: "in_1"}, moved, handoffAuthorization{}, true)
	if !strings.Contains(got, "引用已过期") || !strings.Contains(got, "当前断言 v3") {
		t.Fatalf("a changed claim is not called out:\n%s", got)
	}
	if strings.Contains(got, "UNVERIFIED-BODY") {
		t.Fatalf("a changed claim printed the current body:\n%s", got)
	}

	// Withdrawal is the same class: keep the receipt's identity, do not present
	// the retracted text as usable.
	withdrawn := verified
	withdrawn.Status = "withdrawn"
	withdrawnDB := store.DB{Facts: []model.Fact{withdrawn}, Evidence: db.Evidence}
	got = handoffSection(withdrawnDB, model.Intent{ID: "in_1"}, h, handoffAuthorization{}, true)
	if !strings.Contains(got, "引用已过期") || strings.Contains(got, "UNVERIFIED-BODY") {
		t.Fatalf("a withdrawn cited fact was presented as usable:\n%s", got)
	}
}
