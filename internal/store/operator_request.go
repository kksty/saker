package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kksty/saker/internal/model"
)

// ErrOperatorRequestPending means a concrete request already waits for the
// operator; a second one would duplicate the decision instead of adding to it.
var ErrOperatorRequestPending = errors.New("已有待处理的操作员请求")

// OperatorRequests returns detached rows, pending first and newest first. It is
// control state: the board/report/review paths never read it.
func (s *Store) OperatorRequests() []model.OperatorRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]model.OperatorRequest(nil), s.db.OperatorRequests...)
	sort.SliceStable(out, func(i, j int) bool {
		pi := out[i].Status == model.OperatorRequestPending
		pj := out[j].Status == model.OperatorRequestPending
		if pi != pj {
			return pi
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (s *Store) PendingOperatorRequests() []model.OperatorRequest {
	out := []model.OperatorRequest{}
	for _, r := range s.OperatorRequests() {
		if r.Status == model.OperatorRequestPending {
			out = append(out, r)
		}
	}
	return out
}

func (s *Store) GetOperatorRequest(id string) (model.OperatorRequest, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.db.OperatorRequests {
		if r.ID == id {
			return r, true
		}
	}
	return model.OperatorRequest{}, false
}

// CreateOperatorRequest files a concrete authorization request on the
// commander's (or operator's) behalf. It never creates a second pending row: an
// existing generic fallback is filled in place, so the operator sees one
// evolving request instead of a generic row plus a detailed duplicate. A
// pending concrete request is a conflict, because only the operator can resolve
// or dismiss it.
func (s *Store) CreateOperatorRequest(action, asset, impact, authorization, requestedBy string) (model.OperatorRequest, bool, error) {
	action, asset = strings.TrimSpace(action), strings.TrimSpace(asset)
	impact, authorization = strings.TrimSpace(impact), strings.TrimSpace(authorization)
	if action == "" {
		return model.OperatorRequest{}, false, fmt.Errorf("授权请求必须写 action：说清需要操作员决定什么")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.OperatorRequests {
		r := &s.db.OperatorRequests[i]
		if r.Status != model.OperatorRequestPending {
			continue
		}
		if !r.Generic {
			return model.OperatorRequest{}, false, fmt.Errorf("%w：%s", ErrOperatorRequestPending, r.ID)
		}
		r.Action, r.Asset, r.Impact, r.Authorization = action, asset, impact, authorization
		r.Generic = false
		r.RequestedBy = requestedBy
		r.DetailRequested = false
		r.UpdatedAt = time.Now()
		out := *r
		return out, false, s.flushNoRev("operator_request")
	}
	now := time.Now()
	r := model.OperatorRequest{
		ID:            s.uniqueIDLocked("or"),
		Action:        action,
		Asset:         asset,
		Impact:        impact,
		Authorization: authorization,
		Status:        model.OperatorRequestPending,
		RequestedBy:   requestedBy,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	s.db.OperatorRequests = append(s.db.OperatorRequests, r)
	return r, true, s.flushNoRev("operator_request")
}

// EnsureIdleOperatorRequest creates the generic fallback once per idle episode.
// It is idempotent while any request is still pending: a detailed request made
// by the commander is not overwritten by a new generic one.
func (s *Store) EnsureIdleOperatorRequest() (model.OperatorRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.db.OperatorRequests {
		if r.Status == model.OperatorRequestPending {
			return r, false, nil
		}
	}
	now := time.Now()
	r := model.OperatorRequest{
		ID:            s.uniqueIDLocked("or"),
		Action:        "目标未达成，没有在办意图",
		Authorization: "请授权下一步动作，或直接给出新意图",
		Status:        model.OperatorRequestPending,
		Generic:       true,
		RequestedBy:   model.OperatorRequestFromDecider,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	s.db.OperatorRequests = append(s.db.OperatorRequests, r)
	if err := s.flushNoRev("operator_request"); err != nil {
		return model.OperatorRequest{}, false, err
	}
	return r, true, nil
}

// UpdateOperatorRequest is the commander's (or operator's) detail fill. It only
// changes a pending request; it never turns the request into an authorization.
func (s *Store) UpdateOperatorRequest(id, action, asset, impact, authorization string) (model.OperatorRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.OperatorRequests {
		r := &s.db.OperatorRequests[i]
		if r.ID != id {
			continue
		}
		if r.Status != model.OperatorRequestPending {
			return model.OperatorRequest{}, fmt.Errorf("请求已%s，不能再改明细", r.Status)
		}
		action, asset = strings.TrimSpace(action), strings.TrimSpace(asset)
		impact, authorization = strings.TrimSpace(impact), strings.TrimSpace(authorization)
		if action == "" && asset == "" && impact == "" && authorization == "" {
			return model.OperatorRequest{}, fmt.Errorf("至少要写清需要操作员做什么")
		}
		r.Action, r.Asset, r.Impact, r.Authorization = action, asset, impact, authorization
		r.Generic = false
		r.RequestedBy = model.OperatorRequestFromCommander
		r.DetailRequested = false
		r.UpdatedAt = time.Now()
		out := *r
		return out, s.flushNoRev("operator_request")
	}
	return model.OperatorRequest{}, fmt.Errorf("请求不存在：%s", id)
}

func (s *Store) ResolveOperatorRequest(id, resolution string) (model.OperatorRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.OperatorRequests {
		r := &s.db.OperatorRequests[i]
		if r.ID != id {
			continue
		}
		if r.Status != model.OperatorRequestPending {
			return model.OperatorRequest{}, fmt.Errorf("请求已%s", r.Status)
		}
		now := time.Now()
		r.Status = model.OperatorRequestResolved
		r.Resolution = strings.TrimSpace(resolution)
		r.ResolvedAt = &now
		r.UpdatedAt = now
		out := *r
		return out, s.flushNoRev("operator_request")
	}
	return model.OperatorRequest{}, fmt.Errorf("请求不存在：%s", id)
}

// DismissIdleOperatorRequests closes the generic fallback when work resumes. A
// concrete request the commander filled is left to the operator.
func (s *Store) DismissIdleOperatorRequests(resolution string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for i := range s.db.OperatorRequests {
		r := &s.db.OperatorRequests[i]
		if r.Status != model.OperatorRequestPending || !r.Generic {
			continue
		}
		now := time.Now()
		r.Status = model.OperatorRequestDismissed
		r.Resolution = strings.TrimSpace(resolution)
		r.ResolvedAt = &now
		r.UpdatedAt = now
		changed = true
	}
	if !changed {
		return nil
	}
	return s.flushNoRev("operator_request")
}

// RequestOperatorDetails marks a pending request for the commander's next
// round. The prompt builder clears the flag once it has carried the request id.
func (s *Store) RequestOperatorDetails(id string) (model.OperatorRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.db.OperatorRequests {
		r := &s.db.OperatorRequests[i]
		if r.ID != id {
			continue
		}
		if r.Status != model.OperatorRequestPending {
			return model.OperatorRequest{}, fmt.Errorf("请求已%s", r.Status)
		}
		r.DetailRequested = true
		r.UpdatedAt = time.Now()
		out := *r
		return out, s.flushNoRev("operator_request")
	}
	return model.OperatorRequest{}, fmt.Errorf("请求不存在：%s", id)
}

func (s *Store) ClearOperatorDetailRequests(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for i := range s.db.OperatorRequests {
		r := &s.db.OperatorRequests[i]
		if !want[r.ID] || !r.DetailRequested {
			continue
		}
		r.DetailRequested = false
		r.UpdatedAt = time.Now()
		changed = true
	}
	if !changed {
		return nil
	}
	return s.flushNoRev("operator_request")
}
