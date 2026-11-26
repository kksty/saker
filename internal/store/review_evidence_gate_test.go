package store

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// 用例①（写入口）：零证据的 vuln/cred 事实，无论判定从哪里来，都不能被写成 verified。
// 这是唯一的写入闸口：手动复核接口、复核 run、批量的提前拒绝都读同一条规则
// （ReviewPassBlocked），所以这里必须是权威判定，其余各点只负责把理由说清楚。
func TestReviewFactRefusesZeroEvidenceFinding(t *testing.T) {
	s := open(t)
	for _, kind := range []string{"vuln", "cred"} {
		f, err := s.AddFact(model.Fact{IntentID: "in_1", Kind: kind, Title: "日志明文记录管理员口令", Origin: model.OriginDeclared})
		if err != nil {
			t.Fatal(err)
		}
		if len(f.EvidenceIDs) != 0 {
			t.Fatalf("fixture cites evidence: %v", f.EvidenceIDs)
		}
		rev, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-zero-evidence"})
		if err == nil {
			t.Fatalf("%s: pass written without evidence: %+v", kind, rev)
		}
		if !strings.Contains(err.Error(), "没有任何原始证据") || !strings.Contains(err.Error(), "先补证据") {
			t.Fatalf("%s: 拒绝理由没说清缺什么：%v", kind, err)
		}
		if got, _ := s.FactByID(f.ID); got.Status != "unverified" {
			t.Fatalf("%s: status = %q after a refused pass, want unverified", kind, got.Status)
		}
		if reviews := s.Reviews(f.ID); len(reviews) != 0 {
			t.Fatalf("%s: refused pass left %d verdicts, want 0", kind, len(reviews))
		}
		// Refusing the endorsement must not make the row unusable: a verdict that
		// says "no material to replay" is still allowed and keeps it unverified.
		if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "needs_evidence", Notes: "没有可重放的材料"}); err != nil {
			t.Fatalf("%s: needs_evidence refused: %v", kind, err)
		}
		if got, _ := s.FactByID(f.ID); got.Status != "unverified" {
			t.Fatalf("%s: needs_evidence changed the row to %q", kind, got.Status)
		}
	}
}

// 用例②（写入口）：同一类事实一旦引用了一份可重放的材料，pass 照旧写入 verified。
func TestReviewFactStillVerifiesAFindingWithEvidence(t *testing.T) {
	s := open(t)
	ev, err := s.AddEvidence(model.Evidence{IntentID: "in_1", URI: "in_1/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFact(model.Fact{IntentID: "in_1", Kind: "vuln", Title: "越权读取他人订单", EvidenceIDs: []string{ev.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-proof"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FactByID(f.ID); got.Status != "verified" {
		t.Fatalf("status = %q, want verified", got.Status)
	}
}
