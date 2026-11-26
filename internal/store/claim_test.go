package store

import (
	"errors"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// A review proves one assertion version. Display-only edits (title) keep it,
// claim edits (summary/doc/evidence) start a new version and unverify the row,
// and a verdict carrying the old version is refused instead of silently
// blessing the new content.
func TestReviewBindsToTheProvenClaimVersion(t *testing.T) {
	s := open(t)
	f, err := s.AddFact(model.Fact{IntentID: "in_1", Kind: "vuln", Title: "越权", Summary: "可读 /admin", EvidenceIDs: []string{proof(t, s, "in_1")}})
	if err != nil {
		t.Fatal(err)
	}
	if f.ClaimVersion != 1 || f.ClaimHash == "" {
		t.Fatalf("new claim identity = v%d %q", f.ClaimVersion, f.ClaimHash)
	}
	pass, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", ClaimVersion: f.ClaimVersion, ClaimHash: f.ClaimHash})
	if err != nil {
		t.Fatal(err)
	}
	if pass.ClaimVersion != 1 || pass.ClaimHash != f.ClaimHash {
		t.Fatalf("review did not record the proven version: %+v", pass)
	}
	if got, _ := s.FactByID(f.ID); got.Status != "verified" {
		t.Fatalf("verdict did not apply: %q", got.Status)
	}

	// The title is a display label: renaming a proven claim does not re-open it.
	if _, err := s.UpdateFact(f.ID, func(x *model.Fact) { x.Title = "越权（改名）" }); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FactByID(f.ID); got.ClaimVersion != 1 || got.Status != "verified" {
		t.Fatalf("display edit reopened the claim: v%d %q", got.ClaimVersion, got.Status)
	}

	// Adding evidence changes what the claim rests on: new version, unverified.
	// (Admitted before the update: the store lock is held inside the callback.)
	second := proof(t, s, "in_1")
	changed, err := s.UpdateFact(f.ID, func(x *model.Fact) {
		x.EvidenceIDs = append(x.EvidenceIDs, second)
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.ClaimVersion != 2 || changed.Status != "unverified" {
		t.Fatalf("claim edit = v%d %q, want v2 unverified", changed.ClaimVersion, changed.Status)
	}
	// The stale verdict (v1) must not be applied to v2.
	if _, err := s.ReviewFact(pass); !errors.Is(err, ErrReviewConflict) {
		t.Fatalf("stale verdict error = %v, want ErrReviewConflict", err)
	}
	if got, _ := s.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("stale verdict changed the row: %q", got.Status)
	}

	// A verdict for the current version is accepted.
	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", ClaimVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FactByID(f.ID); got.Status != "verified" {
		t.Fatalf("current verdict did not apply: %q", got.Status)
	}

	// A zero version is the synchronous human verdict: it binds to the row under
	// the store lock, so it can never disagree with today's content.
	human, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "reject", Notes: "人工判定"})
	if err != nil {
		t.Fatal(err)
	}
	if human.ClaimVersion != 2 || human.ClaimHash != changed.ClaimHash {
		t.Fatalf("human verdict bound to the wrong version: %+v", human)
	}
	if got, _ := s.FactByID(f.ID); got.Status != "rejected" {
		t.Fatalf("human verdict did not apply: %q", got.Status)
	}
}

// The claim fingerprint is the proof subject, not the row. Status, origin,
// provenance and the display title are excluded; the assertion text, evidence
// and recorded context are not.
func TestClaimHashCoversTheAssertionNotTheBookkeeping(t *testing.T) {
	base := model.Fact{Kind: "vuln", Title: "t", Summary: "s", Doc: "d", Confidence: "observed", EntityRef: "host:10.0.0.5"}
	h := claimHash(base)
	bookkeeping := base
	bookkeeping.Status, bookkeeping.Origin, bookkeeping.SourceRunID = "verified", model.OriginDeclared, "r_1"
	bookkeeping.DeclarationRef, bookkeeping.DeclarationID, bookkeeping.ID = ".tw/facts.jsonl#bytes=1-2", "obs", "f_1"
	bookkeeping.Title = "another display label"
	bookkeeping.Severity = model.SeverityHigh
	if claimHash(bookkeeping) != h {
		t.Fatal("bookkeeping fields changed the claim fingerprint")
	}
	for _, edit := range []func(*model.Fact){
		func(f *model.Fact) { f.Summary = "s2" },
		func(f *model.Fact) { f.Doc = "d2" },
		func(f *model.Fact) { f.Kind = "cred" },
		func(f *model.Fact) { f.EvidenceIDs = []string{"e_1"} },
		func(f *model.Fact) { f.Tags = []string{"t1"} },
		func(f *model.Fact) { f.EntityRef = "host:10.0.0.6" },
		func(f *model.Fact) { f.ExecutionContextRef = "browser:ac_1" },
	} {
		edited := base
		edit(&edited)
		if claimHash(edited) == h {
			t.Fatalf("assertion edit did not change the claim fingerprint: %+v", edited)
		}
	}
}

// Prove a declaration id continues one observation even when the later line
// omits the source ref. The old rule only adopted in the other direction, so
// the same observation became a second fact (9006 write-probe audit).
func TestFindDeclarationAdoptsAcrossSourceRefOmission(t *testing.T) {
	s := open(t)
	withRef, err := s.AddFact(model.Fact{
		IntentID: "in_1", Kind: "note", Title: "同一观察",
		DeclarationID: "d_1", SourceRef: "report:r1#f1",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.FindDeclaration("in_1", "", "d_1")
	if !ok || got.ID != withRef.ID {
		t.Fatalf("omitted source_ref did not continue %s: %+v ok=%v", withRef.ID, got, ok)
	}

	// A different explicit source is a different observation, even under the
	// same (reused) declaration id.
	if got, ok := s.FindDeclaration("in_1", "report:r1#f2", "d_1"); ok {
		t.Fatalf("different source adopted %s", got.ID)
	}

	// Adoption stays inside the declaring intent.
	if got, ok := s.FindDeclaration("in_2", "", "d_1"); ok {
		t.Fatalf("cross-intent adoption: %s", got.ID)
	}

	// When both rows already exist (the historical duplicate), an exact source
	// match wins, and an ambiguous declaration id prefers the row without one
	// so the stronger provenance is not overwritten.
	withoutRef, err := s.AddFact(model.Fact{
		IntentID: "in_1", Kind: "note", Title: "同一观察（旧重复行）", DeclarationID: "d_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FindDeclaration("in_1", "", "d_1"); got.ID != withoutRef.ID {
		t.Fatalf("ambiguous declaration picked %s, want ref-less %s", got.ID, withoutRef.ID)
	}
	if got, _ := s.FindDeclaration("in_1", "report:r1#f1", "d_1"); got.ID != withRef.ID {
		t.Fatalf("exact source match = %s, want %s", got.ID, withRef.ID)
	}
}
