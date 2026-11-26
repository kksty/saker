package ledger

// This file owns the one identity-based upsert of a parsed handoff line. Both
// write paths that can see a declaration — the live fact watcher while the run
// is still going, and Ingestor.OnFinish when it settles — must call it, because
// two copies of "same identity means same observation" is how the live view and
// the settled ledger start disagreeing (F8).

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// ApplyResult reports what one ApplyDeclaration committed.
type ApplyResult struct {
	FactID string
	// Created is true for a declaration identity the ledger had not seen.
	Created bool
	// Changed is true when the committed row was created or a stored field
	// changed. An identical re-read (the live watcher and the finish settlement
	// reading the same line) reports false and writes nothing, so it cannot bump
	// the claim version, invalidate a review, or notify related directions again.
	Changed bool
}

// ApplyDeclaration applies one parsed handoff line to the ledger.
//
// Identity is the writer's — source ref first, then declaration id — never the
// display title: re-declaring the same observation updates that row; a new
// identity is a new fact even when the title repeats (D74). The field set
// written on update is exactly the settlement path's; the store decides whether
// the claim actually changed and only then starts a new assertion version
// (store.UpdateFact). Title and kind are deliberately not rewritten on update:
// a different kind or display name under the same writer identity is the same
// observation, and the settlement path never treated it as a new assertion.
//
// evidenceIDs is nil on the live path: the run is still going, no file has been
// preserved yet, and evidence an earlier settlement attached must survive until
// the finish path replaces it. Settlement passes the ids it just preserved
// (possibly none); they become the authoritative evidence set for this
// declaration.
func ApplyDeclaration(st *store.Store, intentID, runID string, df DeclaredFact, evidenceIDs []string) (ApplyResult, error) {
	if st == nil {
		return ApplyResult{}, fmt.Errorf("ledger: apply declaration without a store")
	}
	summary := strings.TrimSpace(df.Summary)
	// A declaration is "by this run" only inside the run's own handoff range: a
	// retry in the same case re-reads lines an earlier run wrote, and attributing
	// those to the retry would rewrite history.
	declaredBy := ""
	if run, ok := st.GetRun(runID); ok && run.DeclarationStart != nil && df.Offset >= *run.DeclarationStart {
		declaredBy = runID
	}

	existing, found := st.FindDeclaration(intentID, df.SourceRef, df.DeclarationID)
	if !found {
		f, err := st.AddFact(model.Fact{
			DeclarationID:       df.DeclarationID,
			ObservedAt:          cloneTime(df.ObservedAt),
			ExecutionContextRef: df.ExecutionContextRef,
			EntityRef:           df.EntityRef,
			IntentID:            intentID,
			SourceRunID:         declaredBy,
			DeclarationRef:      df.Location,
			Kind:                df.Kind,
			Title:               df.Title,
			Summary:             summary,
			Doc:                 df.Doc,
			DeclaredConfidence:  df.Confidence,
			Severity:            df.Severity,
			Proof:               df.Proof,
			Tags:                cloneList(df.Tags),
			SourceRef:           df.SourceRef,
			Origin:              model.OriginDeclared,
			EvidenceIDs:         cloneList(evidenceIDs),
			PendingEvidence:     pendingEvidence(evidenceIDs, df.Evidence, nil),
			Refs:                cloneList(df.Refs),
		})
		if err != nil {
			return ApplyResult{}, err
		}
		return ApplyResult{FactID: f.ID, Created: true, Changed: true}, nil
	}

	next := declarationUpdate(existing, df, declaredBy, summary, evidenceIDs)
	if sameDeclarationFields(existing, next) {
		return ApplyResult{FactID: existing.ID}, nil
	}
	if _, err := st.UpdateFact(existing.ID, func(f *model.Fact) {
		f.SourceRunID = next.SourceRunID
		f.DeclarationRef = next.DeclarationRef
		f.DeclarationID, f.ObservedAt, f.ExecutionContextRef = next.DeclarationID, next.ObservedAt, next.ExecutionContextRef
		f.EntityRef = next.EntityRef
		f.Summary = next.Summary
		f.Doc = next.Doc
		f.DeclaredConfidence = next.DeclaredConfidence
		f.Severity = next.Severity
		f.Proof = next.Proof
		f.Tags = next.Tags
		f.SourceRef = next.SourceRef
		f.Origin = next.Origin
		// A live read never owns the evidence set. The closure runs under the
		// store lock, so leaving the field alone keeps whatever settlement
		// attached (even if it attached between our read and this update).
		if evidenceIDs != nil {
			f.EvidenceIDs = next.EvidenceIDs
		}
		f.PendingEvidence = next.PendingEvidence
		f.Refs = next.Refs
	}); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{FactID: existing.ID, Changed: true}, nil
}

// declarationUpdate is the settlement field set applied to an existing row. It
// exists separately from the store write so the unchanged check and the write
// use the same values.
func declarationUpdate(existing model.Fact, df DeclaredFact, declaredBy, summary string, evidenceIDs []string) model.Fact {
	next := existing
	// Keep the original source run when this read cannot prove ownership and the
	// declaration itself did not change: a later re-read of the same line must
	// not erase which run first declared it.
	if declaredBy != "" || existing.DeclarationRef != df.Location || existing.Doc != df.Doc || existing.Summary != summary {
		next.SourceRunID = declaredBy
	}
	next.DeclarationRef = df.Location
	next.DeclarationID = df.DeclarationID
	next.ObservedAt = cloneTime(df.ObservedAt)
	next.ExecutionContextRef = df.ExecutionContextRef
	next.EntityRef = df.EntityRef
	next.Summary = summary
	next.Doc = df.Doc
	next.DeclaredConfidence = df.Confidence
	// A grade, once declared, is not erased by a later line that omits it: the
	// same rule SourceRef follows. Only a valid declared value updates it.
	if df.Severity != "" {
		next.Severity = df.Severity
	}
	next.Proof = df.Proof
	next.Tags = cloneList(df.Tags)
	// SourceRef is provenance the ledger learned, never a field a later line
	// may blank by omission: the same declaration re-read without a source ref
	// keeps the recorded one. A line that names a source sets it.
	next.SourceRef = existing.SourceRef
	if df.SourceRef != "" {
		next.SourceRef = df.SourceRef
	}
	next.Origin = model.OriginDeclared
	if evidenceIDs != nil {
		next.EvidenceIDs = cloneList(evidenceIDs)
	}
	next.PendingEvidence = pendingEvidence(evidenceIDs, df.Evidence, existing.PendingEvidence)
	next.Refs = cloneList(df.Refs)
	return next
}

// pendingEvidence is the declared-but-not-preserved list shown until the
// settlement attaches the authoritative set. A non-nil settlement list is not
// proof that everything resolved: ingest builds it from the entries it could
// preserve, so a shorter list means declared paths were dropped (missing file,
// out of bounds). Clearing the slot there would turn "declared 3, none kept"
// into "无证据", which reads as if the writer never provided any.
func pendingEvidence(evidenceIDs, declared, existing []string) []string {
	if evidenceIDs == nil {
		// A live re-declaration that only corrects the summary carries no
		// evidence list. Erasing what an earlier line declared would turn the
		// row back into "无证据" while the files are still on disk.
		if len(declared) > 0 {
			return cloneList(declared)
		}
		return cloneList(existing)
	}
	if len(evidenceIDs) < len(declared) {
		return cloneList(declared)
	}
	return nil
}

// sameDeclarationFields compares exactly the fields declarationUpdate writes.
// Empty and nil slices are the same value: the JSON round trip and
// store.detachFact normalise one to the other, and a duplicate read must not
// look like a change because of it.
func sameDeclarationFields(a, b model.Fact) bool {
	if a.SourceRunID != b.SourceRunID || a.DeclarationRef != b.DeclarationRef ||
		a.DeclarationID != b.DeclarationID || a.ExecutionContextRef != b.ExecutionContextRef ||
		a.EntityRef != b.EntityRef || a.Summary != b.Summary || a.Doc != b.Doc ||
		a.DeclaredConfidence != b.DeclaredConfidence || a.Severity != b.Severity ||
		a.Proof != b.Proof || a.SourceRef != b.SourceRef ||
		a.Origin != b.Origin {
		return false
	}
	if (a.ObservedAt == nil) != (b.ObservedAt == nil) {
		return false
	}
	if a.ObservedAt != nil && !a.ObservedAt.Equal(*b.ObservedAt) {
		return false
	}
	return slices.Equal(a.Tags, b.Tags) && slices.Equal(a.EvidenceIDs, b.EvidenceIDs) &&
		slices.Equal(a.PendingEvidence, b.PendingEvidence) && slices.Equal(a.Refs, b.Refs)
}

func cloneList[T any](in []T) []T {
	if len(in) == 0 {
		return nil
	}
	return slices.Clone(in)
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}
