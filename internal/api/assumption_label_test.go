package api

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// The label answers one question the status cannot: was this assertion obtained
// by inference or by observation. It keys off the declared base, so it must
// survive a passing review (status=verified) and must not appear on an observed
// row. Both the brief and the graph read it through model.Fact.Assumption, so
// removing the label from either surface turns this test red.
func TestInferredDeclarationIsLabelledAssumptionInBriefAndGraph(t *testing.T) {
	inferred := model.Fact{
		ID: "f_inferred", Kind: "vuln", Status: "verified",
		DeclaredConfidence: "inferred", Confidence: "inferred",
		Title: "CAS 免跳转登录面", EvidenceIDs: []string{"e1"},
	}
	observed := model.Fact{
		ID: "f_observed", Kind: "vuln", Status: "verified",
		DeclaredConfidence: "observed", Confidence: "observed",
		Title: "同一现场的直接观察", EvidenceIDs: []string{"e1"},
	}
	// A legacy row that predates the field carries the base in Confidence.
	legacy := model.Fact{
		ID: "f_legacy", Kind: "vuln", Status: "unverified",
		Confidence: "inferred", Title: "旧行", EvidenceIDs: []string{"e1"},
	}
	withUnverified := func(f model.Fact) string { return renderFact(f, true) }

	if got := withUnverified(inferred); !strings.Contains(got, model.AssumptionLabel) {
		t.Fatalf("verified inferred row lost the label in the brief:\n%s", got)
	}
	if got := withUnverified(observed); strings.Contains(got, model.AssumptionLabel) {
		t.Fatalf("observed row is labelled as an assumption:\n%s", got)
	}
	if got := withUnverified(legacy); !strings.Contains(got, model.AssumptionLabel) {
		t.Fatalf("legacy row with the base in Confidence is not labelled:\n%s", got)
	}

	g := board.BuildGraph(store.DB{Facts: []model.Fact{inferred, observed, legacy}}, "")
	labels := map[string]string{}
	for _, row := range g.Facts {
		labels[row.ID] = row.Label
	}
	if labels["f_inferred"] != model.AssumptionLabel {
		t.Fatalf("graph label for the inferred row = %q, want %q", labels["f_inferred"], model.AssumptionLabel)
	}
	if labels["f_legacy"] != model.AssumptionLabel {
		t.Fatalf("graph label for the legacy row = %q, want %q", labels["f_legacy"], model.AssumptionLabel)
	}
	if labels["f_observed"] != "" {
		t.Fatalf("graph labelled an observed row: %q", labels["f_observed"])
	}
}
