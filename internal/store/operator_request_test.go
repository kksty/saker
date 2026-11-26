package store

import (
	"errors"
	"testing"

	"github.com/kksty/saker/internal/model"
)

func TestOperatorRequestLifecycleIsIdempotentAndControlState(t *testing.T) {
	s := open(t)
	first, created, err := s.EnsureIdleOperatorRequest()
	if err != nil || !created {
		t.Fatalf("ensure: created=%v err=%v", created, err)
	}
	if first.Status != model.OperatorRequestPending || !first.Generic {
		t.Fatalf("fallback = %+v", first)
	}
	second, created, err := s.EnsureIdleOperatorRequest()
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second ensure duplicated the request: created=%v id=%s err=%v", created, second.ID, err)
	}
	if got := len(s.PendingOperatorRequests()); got != 1 {
		t.Fatalf("pending = %d, want 1", got)
	}

	updated, err := s.UpdateOperatorRequest(first.ID, "对计费接口做受控写入", "36.103.205.127", "可能生成一条测试账单", "允许写业务库并回滚")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Generic || updated.RequestedBy != model.OperatorRequestFromCommander {
		t.Fatalf("update did not turn the fallback into details: %+v", updated)
	}
	if _, _, err := s.EnsureIdleOperatorRequest(); err != nil {
		t.Fatal(err)
	}
	if got := len(s.PendingOperatorRequests()); got != 1 {
		t.Fatalf("a detailed pending request was overwritten: %d", got)
	}

	resolved, err := s.ResolveOperatorRequest(first.ID, "已授权")
	if err != nil || resolved.Status != model.OperatorRequestResolved || resolved.ResolvedAt == nil {
		t.Fatalf("resolve = %+v err=%v", resolved, err)
	}
	if _, err := s.ResolveOperatorRequest(first.ID, "again"); err == nil {
		t.Fatal("a resolved request was resolved twice")
	}
}

func TestDismissOnlyTheGenericFallback(t *testing.T) {
	s := open(t)
	first, _, err := s.EnsureIdleOperatorRequest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateOperatorRequest(first.ID, "动作", "资产", "影响", "授权"); err != nil {
		t.Fatal(err)
	}
	if err := s.DismissIdleOperatorRequests("work_resumed"); err != nil {
		t.Fatal(err)
	}
	if got := len(s.PendingOperatorRequests()); got != 1 {
		t.Fatalf("a detailed request was dismissed with the fallback: %d", got)
	}

	if _, err := s.ResolveOperatorRequest(first.ID, "已处理"); err != nil {
		t.Fatal(err)
	}
	second, created, err := s.EnsureIdleOperatorRequest()
	if err != nil || !created {
		t.Fatalf("after resolving the detailed request, a fallback was not created: created=%v err=%v", created, err)
	}
	if err := s.DismissIdleOperatorRequests("work_resumed"); err != nil {
		t.Fatal(err)
	}
	if got := len(s.PendingOperatorRequests()); got != 0 {
		t.Fatalf("dismiss left %d pending", got)
	}
	if again, ok := s.GetOperatorRequest(second.ID); !ok || again.Status != model.OperatorRequestDismissed {
		t.Fatalf("cancelled fallback = %+v", again)
	}
}

// A direct concrete request reuses the generic fallback instead of adding a
// second pending row; a second concrete request is a conflict, because only the
// operator can resolve the first one.
func TestCreateOperatorRequestReusesTheFallbackAndRejectsADuplicate(t *testing.T) {
	s := open(t)
	fallback, created, err := s.EnsureIdleOperatorRequest()
	if err != nil || !created {
		t.Fatalf("ensure: created=%v err=%v", created, err)
	}
	req, created, err := s.CreateOperatorRequest(
		"对 225.219 的上传接口做受控写入", "36.103.225.219", "可能新增一条测试文件", "允许写并清理",
		model.OperatorRequestFromCommander,
	)
	if err != nil || created || req.ID != fallback.ID {
		t.Fatalf("reuse fallback: created=%v id=%s err=%v", created, req.ID, err)
	}
	if req.Generic || req.RequestedBy != model.OperatorRequestFromCommander || req.Action == "" {
		t.Fatalf("concrete request = %+v", req)
	}
	if _, _, err := s.CreateOperatorRequest("另一个动作", "", "", "", model.OperatorRequestFromCommander); !errors.Is(err, ErrOperatorRequestPending) {
		t.Fatalf("second concrete request = %v, want ErrOperatorRequestPending", err)
	}
	if _, err := s.ResolveOperatorRequest(req.ID, "已授权"); err != nil {
		t.Fatal(err)
	}
	next, created, err := s.CreateOperatorRequest("新动作", "资产", "影响", "授权", model.OperatorRequestFromCommander)
	if err != nil || !created || next.ID == req.ID {
		t.Fatalf("after resolve: created=%v id=%s err=%v", created, next.ID, err)
	}
}
