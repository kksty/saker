package model

import (
	"strings"
	"testing"
)

// One predicate answers "may this playbook be called confirmed". These tests pin
// the four ways its sources can fail and the one way they can pass; every reader
// (prompt, recall, KB list, board, detail endpoint) derives its label from it.

func confirmedPlaybook(sources ...string) Playbook {
	return Playbook{Title: "tactic", Status: PlaybookConfirmed, FromFactIDs: sources}
}

// boundPlaybook is the new-write shape: the confirmation records the claim
// version it accepted from each cited source.
func boundPlaybook(facts map[string]Fact, sources ...string) Playbook {
	p := confirmedPlaybook(sources...)
	p.FromFactClaims = map[string]uint64{}
	for _, id := range sources {
		if f, ok := facts[id]; ok {
			p.FromFactClaims[id] = f.ClaimVersion
		}
	}
	return p
}

func sourceFact(id, status string, evidence bool) Fact {
	f := Fact{ID: id, Kind: "vuln", Title: id, Status: status, ClaimVersion: 1}
	if evidence {
		f.EvidenceIDs = []string{"ev_" + id}
	}
	return f
}

// A source that is not in the ledger cannot support anything: the row must not
// be labelled confirmed just because the writer cited an id-shaped string.
func TestSourceProblemNamesAMissingSourceFact(t *testing.T) {
	facts := FactIndex([]Fact{sourceFact("f_present", "verified", true)})
	p := boundPlaybook(facts, "f_present", "f_gone")
	problem := p.SourceProblem(facts)
	if problem == "" || !strings.Contains(problem, "f_gone") {
		t.Fatalf("missing source not named: %q", problem)
	}
	if p.Confirmed(facts) {
		t.Fatal("a playbook with a missing source was reported confirmed")
	}
}

// Every not-verified state fails: unverified is "waiting for review", rejected is
// "the proof did not hold", withdrawn is "this record should not exist". None of
// them may back a confirmed label.
func TestSourceProblemRejectsEveryUnverifiedState(t *testing.T) {
	for _, status := range []string{"unverified", "rejected", "withdrawn"} {
		t.Run(status, func(t *testing.T) {
			facts := FactIndex([]Fact{sourceFact("f_1", status, true)})
			p := boundPlaybook(facts, "f_1")
			problem := p.SourceProblem(facts)
			if problem == "" || !strings.Contains(problem, "f_1") {
				t.Fatalf("status %s passed with evidence: %q", status, problem)
			}
			if p.Confirmed(facts) {
				t.Fatalf("a %s source still confirmed the playbook", status)
			}
		})
	}
}

// A verified claim without bytes is still not proof of the tactic: the writer
// only asserted it.
func TestSourceProblemRequiresEvidenceOnAVerifiedSource(t *testing.T) {
	facts := FactIndex([]Fact{sourceFact("f_1", "verified", false)})
	p := boundPlaybook(facts, "f_1")
	problem := p.SourceProblem(facts)
	if problem == "" || !strings.Contains(problem, "证据") {
		t.Fatalf("evidence-free verified source passed: %q", problem)
	}
	if p.Confirmed(facts) {
		t.Fatal("a verified source without evidence confirmed the playbook")
	}
}

// One bad source is enough to downgrade the whole row: confirmation is not
// partial, and the first bad source in the declared order is the reason.
func TestOneBadSourceDowngradesTheWholePlaybook(t *testing.T) {
	facts := FactIndex([]Fact{
		sourceFact("f_good", "verified", true),
		sourceFact("f_bad", "unverified", true),
	})
	p := boundPlaybook(facts, "f_good", "f_bad")
	if problem := p.SourceProblem(facts); problem == "" {
		t.Fatal("a partial source set was accepted")
	}
	if p.Confirmed(facts) {
		t.Fatal("partial confirmation was reported as confirmed")
	}
}

func TestConfirmedAcceptsOnlyVerifiedSourcesWithEvidence(t *testing.T) {
	facts := FactIndex([]Fact{
		sourceFact("f_1", "verified", true),
		sourceFact("f_2", "verified", true),
	})
	p := boundPlaybook(facts, "f_1", "f_2")
	if problem := p.SourceProblem(facts); problem != "" {
		t.Fatalf("qualified sources were rejected: %q", problem)
	}
	if !p.Confirmed(facts) {
		t.Fatal("a fully verified playbook was not confirmed")
	}
	// The writer's declared status is part of the answer: a commander nomination
	// never becomes confirmed just because its sources are good.
	candidate := p
	candidate.Status = PlaybookCandidate
	if candidate.Confirmed(facts) {
		t.Fatal("a candidate became confirmed from its sources alone")
	}
}

// The executor records a tactic it just discovered; its source facts are still
// unverified, so the label is a candidate. Zero sources is the same answer: the
// row is kept and recalled, but never labelled confirmed.
func TestZeroSourcePlaybookIsNeverConfirmed(t *testing.T) {
	facts := FactIndex(nil)
	p := confirmedPlaybook()
	if p.Confirmed(facts) {
		t.Fatal("a playbook without any source was confirmed")
	}
}
