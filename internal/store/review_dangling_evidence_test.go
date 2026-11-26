package store

// 闸只判"有没有"、不判"是不是真的"：evidence_ids 非空、但指向账本里不存在的行，
// 旧规则放行——202 之后一条 pass 就把事实写成 verified，报告据此当作证明，而原件
// 根本不存在。悬空 id 与零证据同罪：都没有可重放的材料。
//
// Falsification: 删掉 reviewPassBlockedLocked 里的存在性循环，前两条必须红。

import (
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

const danglingEvidenceID = "e_doesnotexist000000000000000000"

func TestReviewFactRefusesDanglingEvidence(t *testing.T) {
	s := open(t)
	f, err := s.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "引用了不存在的原件",
		Origin: model.OriginDeclared, EvidenceIDs: []string{danglingEvidenceID},
	})
	if err != nil {
		t.Fatal(err)
	}

	rev, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-dangling"})
	if err == nil {
		t.Fatalf("pass written on a dangling reference: %+v", rev)
	}
	// 理由要指名道姓：操作员得知道去修哪一条引用。
	if !strings.Contains(err.Error(), danglingEvidenceID) || !strings.Contains(err.Error(), "不在账本里") {
		t.Fatalf("拒绝理由没指名悬空 id：%v", err)
	}
	if got, _ := s.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("status = %q，want unverified", got.Status)
	}
	if reviews := s.Reviews(f.ID); len(reviews) != 0 {
		t.Fatalf("被拒绝后留了 %d 条判定，want 0", len(reviews))
	}
}

func TestReviewFactRefusesMixedRealAndDanglingEvidence(t *testing.T) {
	s := open(t)
	real, err := s.AddEvidence(model.Evidence{URI: "in_1/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.AddFact(model.Fact{
		IntentID: "in_1", Kind: "cred", Title: "一条真的一条悬空的",
		Origin: model.OriginDeclared, EvidenceIDs: []string{real.ID, danglingEvidenceID},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 有一条真的不等于可以放行：报告会引用那一条悬空的，复核者打不开它。
	if _, err := s.ReviewFact(model.Review{FactID: f.ID, Verdict: "pass", VerifierRun: "run-mixed"}); err == nil {
		t.Fatal("pass written although one cited row does not exist")
	} else if !strings.Contains(err.Error(), danglingEvidenceID) {
		t.Fatalf("拒绝理由没指名悬空的那一条：%v", err)
	}
	if got, _ := s.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("status = %q，want unverified", got.Status)
	}
	if reviews := s.Reviews(f.ID); len(reviews) != 0 {
		t.Fatalf("被拒绝后留了 %d 条判定，want 0", len(reviews))
	}
	// 同一条规则在读入口给空串：真证据照旧可判。
	if reason := s.ReviewPassBlocked(f); reason != "" {
		if !strings.Contains(reason, danglingEvidenceID) {
			t.Fatalf("rule reason lost the dangling id: %q", reason)
		}
	} else {
		t.Fatal("rule passed a fact that cites a missing row")
	}
}
