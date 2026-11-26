package store

// This file owns the one provenance inference the ledger makes: a fact cannot
// look more certain than the premises of the direction that produced it.
//
// The granularity is the direction, not the individual claim. A declaration's
// own from_fact_ids are not recorded (model.Fact has no such field, and the
// declaration contract no longer offers one), so the only edges that exist are:
//
//	up(f)   = the hard premises (intent.FromFactIDs) of the direction that produced f
//	down(f) = facts produced by directions that name f as a hard premise, transitively
//
// The price is stated rather than hidden: one lost premise demotes every fact of
// the same direction, including facts that did not rest on it. The alternative —
// inventing a per-claim edge the writer never recorded — would be a fabricated
// dependency, which is worse than a coarse true one.
//
// Confidence follows the declared base and current premises. Loss of backing
// changes Status and records a reason in the existing version journal.

import (
	"slices"

	"github.com/kksty/saker/internal/model"
)

// premiseLostReason prefixes the version-journal reason written on the rows a
// lost premise used to back.
const premiseLostReason = "premise_lost:"

// premiseChangedReason prefixes the journal reason written when a premise's
// assertion changed under rows that were verified against the old one. A verdict
// on v1 says nothing about v2, so the backing is gone even though the row is
// still there.
const premiseChangedReason = "premise_changed:"

// upstreamUnverifiedLocked returns the first premise of the direction that
// produced f whose status is not verified.
//
// A direction with no premises (a first touch) has none: the row speaks for
// itself. A premise id that names no row counts as unverified — nothing in the
// ledger vouches for it, and readiness already refuses to dispatch on it.
func (s *Store) upstreamUnverifiedLocked(f model.Fact) (string, bool) {
	if f.IntentID == "" {
		return "", false
	}
	for i := range s.db.Intents {
		in := &s.db.Intents[i]
		if in.ID == f.IntentID {
			return firstUnverifiedPremise(f.ID, in.FromFactIDs, s.factByIDLocked)
		}
	}
	return "", false
}

func firstUnverifiedPremise(factID string, premises []string, lookup func(string) *model.Fact) (string, bool) {
	for _, id := range premises {
		if id == "" || id == factID {
			continue
		}
		if p := lookup(id); p == nil || p.Status != "verified" {
			return id, true
		}
	}
	return "", false
}

// factByIDLocked returns the live row for id. Caller holds the lock.
func (s *Store) factByIDLocked(id string) *model.Fact {
	for i := range s.db.Facts {
		if s.db.Facts[i].ID == id {
			return &s.db.Facts[i]
		}
	}
	return nil
}

// refreshDownstreamLocked indexes the current ledger once so a long chain
// does not make each downstream lookup rescan the full ledger. If backing was
// lost, clear the affected statuses before deriving any of the new labels.
func (s *Store) refreshDownstreamLocked(id string, lost bool) []string {
	if id == "" || !slices.ContainsFunc(s.db.Intents, func(in model.Intent) bool {
		return slices.Contains(in.FromFactIDs, id)
	}) {
		return nil
	}
	consumers := make(map[string][]string)
	premises := make(map[string][]string, len(s.db.Intents))
	for _, in := range s.db.Intents {
		premises[in.ID] = in.FromFactIDs
		for _, premise := range in.FromFactIDs {
			consumers[premise] = append(consumers[premise], in.ID)
		}
	}
	facts := make(map[string]*model.Fact, len(s.db.Facts))
	outputs := make(map[string][]*model.Fact)
	for i := range s.db.Facts {
		f := &s.db.Facts[i]
		facts[f.ID] = f
		outputs[f.IntentID] = append(outputs[f.IntentID], f)
	}

	seenFact := map[string]bool{id: true}
	seenIntent := map[string]bool{}
	queue := []string{id}
	for i := 0; i < len(queue); i++ {
		for _, intentID := range consumers[queue[i]] {
			if seenIntent[intentID] {
				continue
			}
			seenIntent[intentID] = true
			for _, f := range outputs[intentID] {
				if seenFact[f.ID] {
					continue
				}
				seenFact[f.ID] = true
				queue = append(queue, f.ID)
			}
		}
	}

	var changed []string
	if lost {
		for _, downstreamID := range queue[1:] {
			f := facts[downstreamID]
			if f.Status == "verified" {
				f.Status = "unverified"
				changed = append(changed, downstreamID)
			}
		}
	}
	lookup := func(id string) *model.Fact { return facts[id] }
	for _, downstreamID := range queue[1:] {
		f := facts[downstreamID]
		_, unverified := firstUnverifiedPremise(f.ID, premises[f.IntentID], lookup)
		setDerivedConfidence(f, unverified)
	}
	return changed
}

// declaredBase preserves the historical confidence of rows written before
// the separate declaration field existed.
func declaredBase(f *model.Fact) string {
	if f.DeclaredConfidence != "" {
		return f.DeclaredConfidence
	}
	if f.Confidence == "inferred" {
		return "inferred"
	}
	return "observed"
}

func (s *Store) deriveConfidenceLocked(f *model.Fact) {
	_, unverified := s.upstreamUnverifiedLocked(*f)
	setDerivedConfidence(f, unverified)
}

// Changing the effective label leaves the declared base and claim hash intact.
func setDerivedConfidence(f *model.Fact, unverified bool) {
	base := declaredBase(f)
	f.DeclaredConfidence = base
	f.Confidence = base
	if unverified {
		f.Confidence = "inferred"
	}
}

// flushFactChangesLocked commits pending in-memory writes and labels the rows in
// ids with reason in the version journal. The journal is the only place a
// reader can see *why* a verified row went back to unverified: Status alone
// cannot tell "a premise was withdrawn" from "nobody ever ran a review".
//
// ids empty writes no reason at all, so an ordinary status change is never
// labelled as a propagation.
func (s *Store) flushFactChangesLocked(reason string, ids []string, kinds ...string) error {
	if len(ids) > 0 && reason != "" {
		s.changeReason = reason
		s.changeReasonIDs = make(map[string]bool, len(ids))
		for _, id := range ids {
			s.changeReasonIDs[id] = true
		}
	}
	err := s.flush(kinds...)
	s.changeReason, s.changeReasonIDs = "", nil
	return err
}
