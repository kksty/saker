package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/model"
)

// 用例①（手动单条复核接口）：零证据的 vuln 结论必须在启动复核之前被拒绝，理由说清缺什么。
// 修复前的同一请求返回 202 并把事实写成 verified（复现输出见提交说明）。
func TestManualVerifyRefusesZeroEvidenceVuln(t *testing.T) {
	ex := &batchExec{body: `{"verdict":"pass","notes":"重跑一致","proof":"p"}`}
	_, st, mux := newBatchServer(t, ex)
	f, err := st.AddFact(model.Fact{
		IntentID: "in_1", Kind: "vuln",
		Title:  "日志明文记录管理员登录口令",
		Origin: model.OriginDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}

	code, out := doPost(t, mux, "/api/facts/"+f.ID+"/verify")
	t.Logf("POST /api/facts/%s/verify -> %d %v", f.ID, code, out)
	if code != http.StatusConflict {
		t.Fatalf("零证据复核 = %d %v，want 409（拒绝而不是排队一次模型 run）", code, out)
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "没有任何原始证据") || !strings.Contains(msg, "先补证据") {
		t.Fatalf("拒绝理由没说清缺什么：%q", msg)
	}

	// A refusal that still changed the ledger would be a silent verdict.
	if got, _ := st.FactByID(f.ID); got.Status != "unverified" {
		t.Fatalf("被拒绝后状态 = %q，want unverified", got.Status)
	}
	if revs := st.Reviews(f.ID); len(revs) != 0 {
		t.Fatalf("被拒绝后写了 %d 条判定，want 0", len(revs))
	}
	if runs := st.Runs(); len(runs) != 0 {
		t.Fatalf("被拒绝后仍启动 %d 个 run，want 0（没有可买的复核）", len(runs))
	}
}

// 用例②：有原始证据的 vuln 事实照旧走通复核，行为不变。
func TestManualVerifyStillPassesWithEvidence(t *testing.T) {
	ex := &batchExec{body: `{"verdict":"pass","notes":"重跑一致","proof":"p"}`}
	_, st, mux := newBatchServer(t, ex)
	f := addVerifyFact(t, st, "vuln", "unverified")

	code, out := doPost(t, mux, "/api/facts/"+f.ID+"/verify")
	t.Logf("POST /api/facts/%s/verify -> %d %v", f.ID, code, out)
	if code != http.StatusAccepted {
		t.Fatalf("有证据复核 = %d %v，want 202", code, out)
	}
	waitFor(t, 5*time.Second, func() bool {
		cur, _ := st.FactByID(f.ID)
		return cur.Status == "verified"
	})
	revs := st.Reviews(f.ID)
	if len(revs) != 1 || revs[0].Verdict != "pass" {
		t.Fatalf("reviews = %+v，want 一条 pass", revs)
	}
}
