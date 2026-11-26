package store

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// Withdrawal is a state change on the existing row, not a delete: the fact, its
// evidence and its history stay, it becomes unusable, a repeated withdrawal is
// inert, and a later correction clears the mark for the new assertion only.
func TestWithdrawFactKeepsTheRowAndBindsToTheClaimVersion(t *testing.T) {
	s := open(t)
	f, err := s.AddFact(model.Fact{IntentID: "in_1", Kind: "note", Title: "9006 测试B1", Summary: "写入探针"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithdrawFact(f.ID, "operator", "", "   "); err == nil {
		t.Fatal("empty reason accepted")
	}
	if _, err := s.WithdrawFact("f_missing", "operator", "", "理由"); err == nil {
		t.Fatal("withdrawing a missing fact succeeded")
	}

	got, err := s.WithdrawFact(f.ID, "pt_in_1", "r_1", "写入通道探针，不承载结论")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "withdrawn" || got.RetractedBy != "pt_in_1" || got.RetractedRunID != "r_1" ||
		got.RetractionReason == "" || got.RetractedAt == nil || got.RetractedClaimVersion != f.ClaimVersion {
		t.Fatalf("withdrawn fact = %+v", got)
	}
	if _, ok := s.FactByID(f.ID); !ok {
		t.Fatal("withdrawal removed the row")
	}
	if issue := model.PremiseIssue(got, model.PremiseVerifiedOnly); issue != "事实已撤回" {
		t.Fatalf("premise issue = %q", issue)
	}
	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "needs_evidence", Notes: "补证据"}); err == nil ||
		!strings.Contains(err.Error(), "已撤回") {
		t.Fatalf("review after withdrawal = %v", err)
	}

	// A retry may not rewrite the first actor or reason.
	again, err := s.WithdrawFact(f.ID, "pt_other", "r_2", "另一个理由")
	if err != nil {
		t.Fatal(err)
	}
	if again.RetractedBy != "pt_in_1" || again.RetractionReason != "写入通道探针，不承载结论" {
		t.Fatalf("repeated withdrawal overwrote the record: %+v", again)
	}

	// New content is a new assertion: the old withdrawal does not bless it.
	corrected, err := s.UpdateFact(f.ID, func(x *model.Fact) { x.Summary = "更正后的断言" })
	if err != nil {
		t.Fatal(err)
	}
	if corrected.Status != "unverified" || corrected.ClaimVersion != f.ClaimVersion+1 ||
		corrected.RetractedAt != nil || corrected.RetractedBy != "" || corrected.RetractionReason != "" ||
		corrected.RetractedClaimVersion != 0 {
		t.Fatalf("correction did not clear the withdrawal: %+v", corrected)
	}
}
