package api

// 两条入口对"引用了一条不存在的原件"的反应，实测记录下来，不靠推断：
//   - 单条（POST /api/facts/{id}/verify）：409，理由指名悬空 id；不买 run、不写判定。
//   - 批量（POST /api/facts/verify-pending）：入口先用 CanReview 过滤，这条进不了
//     批次，返回 queued=0；不买 run、不写判定、不动意图。
//   - 自动泵：候选能进来（ReviewEligible 不看证据），在 Verifier.Run 的准入被拒，
//     计一次失败并进入冷却，其余候选照跑——不是整批失败。
//
// Falsification: 删掉 reviewPassBlockedLocked 里的存在性循环，单条与混合两条必须红。

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
)

const danglingEvidenceID = "e_doesnotexist000000000000000000"

func TestManualVerifyRefusesDanglingEvidence(t *testing.T) {
	ex := &batchExec{body: `{"verdict":"pass","notes":"重跑一致","proof":"p"}`}
	_, st, mux := newBatchServer(t, ex)
	f, err := st.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "引用了不存在的原件",
		Origin: model.OriginDeclared, EvidenceIDs: []string{danglingEvidenceID},
	})
	if err != nil {
		t.Fatal(err)
	}

	code, out := doPost(t, mux, "/api/facts/"+f.ID+"/verify")
	t.Logf("POST /api/facts/%s/verify -> %d %v", f.ID, code, out)
	if code != http.StatusConflict {
		t.Fatalf("悬空引用复核 = %d %v，want 409", code, out)
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, danglingEvidenceID) || !strings.Contains(msg, "不在账本里") {
		t.Fatalf("拒绝理由没指名悬空 id：%q", msg)
	}
	if got, _ := st.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("被拒绝后 status = %q，want unverified", got.Status)
	}
	if reviews := st.Reviews(f.ID); len(reviews) != 0 {
		t.Fatalf("被拒绝后写了 %d 条判定，want 0", len(reviews))
	}
	if runs := st.Runs(); len(runs) != 0 {
		t.Fatalf("被拒绝后仍启动 %d 个 run，want 0", len(runs))
	}
}

func TestManualVerifyRefusesMixedRealAndDangling(t *testing.T) {
	ex := &batchExec{body: `{"verdict":"pass","notes":"重跑一致","proof":"p"}`}
	_, st, mux := newBatchServer(t, ex)
	real, err := st.AddEvidence(model.Evidence{URI: "in_1/proof.txt", Name: "proof.txt"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{
		IntentID: "in_1", Kind: "cred", Title: "一条真的一条悬空的",
		Origin: model.OriginDeclared, EvidenceIDs: []string{real.ID, danglingEvidenceID},
	})
	if err != nil {
		t.Fatal(err)
	}

	code, out := doPost(t, mux, "/api/facts/"+f.ID+"/verify")
	t.Logf("POST /api/facts/%s/verify -> %d %v", f.ID, code, out)
	if code != http.StatusConflict {
		t.Fatalf("真+悬空复核 = %d %v，want 409（一条真的不等于可以放行）", code, out)
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, danglingEvidenceID) {
		t.Fatalf("拒绝理由没指名悬空的那一条：%q", msg)
	}
	if got, _ := st.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("status = %q，want unverified", got.Status)
	}
	if reviews := st.Reviews(f.ID); len(reviews) != 0 {
		t.Fatalf("写了 %d 条判定，want 0", len(reviews))
	}
	if runs := st.Runs(); len(runs) != 0 {
		t.Fatalf("启动了 %d 个 run，want 0", len(runs))
	}
}

// 批量入口先用 CanReview 过滤：悬空引用的那条进不了批次（queued=0），既不花 run
// 也不写判定，意图状态不动。整批不会因为这一条失败。
func TestBatchSkipsDanglingEvidenceSilently(t *testing.T) {
	ex := &batchExec{body: `{"verdict":"pass","notes":"重跑一致","proof":"p"}`}
	_, st, mux := newBatchServer(t, ex)
	f, err := st.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln", Title: "批量里的悬空引用",
		Origin: model.OriginDeclared, EvidenceIDs: []string{danglingEvidenceID},
	})
	if err != nil {
		t.Fatal(err)
	}

	code, out := doPost(t, mux, "/api/facts/verify-pending")
	t.Logf("POST /api/facts/verify-pending -> %d %v", code, out)
	if code != http.StatusOK {
		t.Fatalf("批量 = %d %v，want 200", code, out)
	}
	if queued, _ := out["queued"].(float64); queued != 0 {
		t.Fatalf("批量排了 %v 条，want 0（这条不该进批次）", out["queued"])
	}
	if got, _ := st.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("status = %q，want unverified", got.Status)
	}
	if reviews := st.Reviews(f.ID); len(reviews) != 0 {
		t.Fatalf("写了 %d 条判定，want 0", len(reviews))
	}
	if runs := st.Runs(); len(runs) != 0 {
		t.Fatalf("启动了 %d 个 run，want 0", len(runs))
	}
}

// 自动泵：候选会被排进来，准入拒绝后计一次失败、进入冷却（不每次唤醒重试），
// 其余候选照跑，意图状态不动。
func TestAutoPumpSkipsDanglingEvidenceAndCoolsDown(t *testing.T) {
	ex := &reviewProbeExec{}
	s, st := newAutoVerifyServer(t, ex)
	in, err := st.CreateIntent(model.Intent{Title: "悬空引用意图"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.AddFact(model.Fact{
		IntentID: in.ID, Kind: "vuln", Title: "自动泵里的悬空引用",
		Origin: model.OriginDeclared, EvidenceIDs: []string{danglingEvidenceID},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 前置：它确实进了自动队列，否则下面的断言会因为"根本没被考虑"而假绿。
	if rows, _ := st.ReviewCandidates(in.ID, "", 10, s.reviewPolicy()); len(rows) != 1 || rows[0].ID != f.ID {
		t.Fatalf("fixture: 悬空引用的事实没有进自动队列：%+v", rows)
	}

	s.autoVerify(in.ID)

	if got, _ := st.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("自动泵后 status = %q，want unverified", got.Status)
	}
	if reviews := st.Reviews(f.ID); len(reviews) != 0 {
		t.Fatalf("自动泵写了 %d 条判定，want 0", len(reviews))
	}
	if n := ex.runs(); n != 0 {
		t.Fatalf("自动泵买了 %d 次模型 run，want 0（没有可重放的材料）", n)
	}
	if _, cooling := s.reviewCoolingDown(f, time.Now()); !cooling {
		t.Fatal("准入被拒的候选没有进入冷却：下一次唤醒会再排一遍")
	}
}
