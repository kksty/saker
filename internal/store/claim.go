package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/kksty/saker/internal/model"
)

// ErrReviewConflict means a verdict was judged against an assertion that has
// since changed. Applying it to the new row would let an old proof speak for
// new content, so the write is refused and the fact stays unverified.
var ErrReviewConflict = errors.New("复核依据的断言版本已变化：旧结论不能落到新版本上")

// claimHash fingerprints the part of a fact a verifier proves. The display
// title, review status, origin, provenance bookkeeping and identity are
// excluded: renaming a row or recording a verdict must not invalidate the same
// proof. Evidence is included because adding evidence changes what the claim
// rests on — that is a new assertion, not the old one with a nicer label.
//
// The confidence slot holds the *declared* base, not the derived label. A label
// that moves when a premise gains or loses verification is not an assertion
// change, so hashing the effective value would unverify rows for a derivation
// they never made. Rows written before DeclaredConfidence existed fall back to
// their current Confidence, which is exactly the value this slot held before, so
// their hashes do not move.
func claimHash(f model.Fact) string {
	tags, evidence, refs := slices.Clone(f.Tags), slices.Clone(f.EvidenceIDs), slices.Clone(f.Refs)
	slices.Sort(tags)
	slices.Sort(evidence)
	slices.SortFunc(refs, func(a, b model.Ref) int {
		if a.Scheme != b.Scheme {
			return strings.Compare(a.Scheme, b.Scheme)
		}
		return strings.Compare(a.Locator, b.Locator)
	})
	base := f.DeclaredConfidence
	if base == "" {
		base = f.Confidence
	}
	// The struct field order is the hash contract: reordering fields changes
	// every hash and would silently unverify the whole ledger.
	raw, _ := json.Marshal(struct {
		Kind, Entity, Summary, Doc, Confidence, Proof, Context string
		Tags, Evidence                                         []string
		Refs                                                   []model.Ref
		ObservedAt                                             any
	}{f.Kind, f.EntityRef, f.Summary, f.Doc, base, f.Proof, f.ExecutionContextRef, tags, evidence, refs, f.ObservedAt})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// FindDeclaration resolves the explicit observation a declaration continues.
//
// Identity comes from the writer, never from the display text: a source ref is
// the strongest identity (an audit finding), otherwise the declaration id.
// Two different declarations with the same title are two observations, which is
// exactly what the old (intent, kind, title) rule could not express.
//
// A source ref is learned provenance, not a one-way upgrade: a line that later
// gains one adopts the row recorded for the same declaration id, and a line
// that omits one still continues that row. The previous rule only adopted in
// the first direction, so the same declaration id became a second fact when the
// later line dropped its source_ref (audit of 9006, 2026-11-19). Provenance is
// never erased by an omission: apply.declarationUpdate keeps the recorded
// source ref when the incoming line has none.
//
// A line naming a *different* source ref is not the same observation even when
// it reuses the declaration id: two explicit source identities must not
// collapse into one row, so that case still creates a separate fact.
func (s *Store) FindDeclaration(intentID, sourceRef, declarationID string) (model.Fact, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sourceRef, declarationID = strings.TrimSpace(sourceRef), strings.TrimSpace(declarationID)
	if sourceRef == "" && declarationID == "" {
		return model.Fact{}, false
	}
	var adopt, adoptWithoutRef *model.Fact
	for i := range s.db.Facts {
		f := &s.db.Facts[i]
		if f.IntentID != intentID {
			continue
		}
		if sourceRef != "" && f.SourceRef == sourceRef {
			return detachFact(*f), true
		}
		if declarationID == "" || f.DeclarationID != declarationID {
			continue
		}
		if sourceRef != "" && f.SourceRef != "" {
			// Both sides name a source and they differ: not the same
			// observation, even under a reused declaration id.
			continue
		}
		if adopt == nil {
			adopt = f
		}
		if f.SourceRef == "" {
			adoptWithoutRef = f
		}
	}
	// Prefer the row with the weaker identity: the incoming line can describe
	// it without overwriting a stronger recorded provenance.
	if adoptWithoutRef != nil {
		return detachFact(*adoptWithoutRef), true
	}
	if adopt != nil {
		return detachFact(*adopt), true
	}
	return model.Fact{}, false
}

// FindDerivedFact resolves the one derived row a completed run refreshes. It
// matches only derived notes: an explicit observation with the same title is a
// different fact, and merging it here would let a derived note overwrite (or be
// hidden by) a declaration the executor made on purpose. This is the whole
// remaining role of title matching, and it is deliberately unable to see
// declared rows.
func (s *Store) FindDerivedFact(intentID, kind, title string) (model.Fact, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.db.Facts {
		f := &s.db.Facts[i]
		if f.IntentID != intentID || f.Origin != model.OriginDerived || f.SourceRef != "" {
			continue
		}
		if f.Kind == kind && f.Title == title {
			return detachFact(*f), true
		}
	}
	return model.Fact{}, false
}
