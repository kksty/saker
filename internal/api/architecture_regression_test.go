package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kksty/saker/internal/aopexec"
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

func admissionServer(t *testing.T) (*Server, *blockingExec) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	ex := &blockingExec{}
	s := New(st, ex, dir, filepath.Join(dir, "cases"))
	s.Timeout = 0
	t.Cleanup(func() { s.SetPaused(true); s.CancelAll(); s.WaitIdle(3 * time.Second) })
	return s, ex
}

func TestCustomPromptCannotBypassAdmission(t *testing.T) {
	for _, gate := range []string{"project", "instance"} {
		t.Run(gate, func(t *testing.T) {
			s, _ := admissionServer(t)
			switch gate {
			case "project":
				s.MaxRuns = 1
				_, _ = s.Store.AddRun(model.Run{IntentID: "busy"})
			case "instance":
				s.MaxWorkers = 1
				s.GlobalRunning = func() int { return 1 }

			}
			in, err := s.Store.CreateIntent(model.Intent{Title: "fresh"})
			if err != nil {
				t.Fatal(err)
			}
			before := len(s.Store.Runs())
			if _, err := s.startRunWith(in.ID, "custom fresh brief", "", false, true); err == nil {
				t.Fatal("custom prompt bypassed admission")
			}
			if len(s.Store.Runs()) != before {
				t.Fatal("refused dispatch created a run")
			}
		})
	}
}

func TestCrossProjectAdmissionReservesInstanceAndProfileCapacity(t *testing.T) {
	for _, gate := range []string{"instance"} {
		t.Run(gate, func(t *testing.T) {
			a, _ := admissionServer(t)
			b, _ := admissionServer(t)
			var admission sync.Mutex
			count := func() int { return a.RunningIntentCount() + b.RunningIntentCount() }
			for _, s := range []*Server{a, b} {
				s.InstanceDispatch = &admission
				s.MaxWorkers = 1
				s.GlobalRunning = count

			}
			ia, _ := a.Store.CreateIntent(model.Intent{Title: "a"})
			ib, _ := b.Store.CreateIntent(model.Intent{Title: "b"})
			start := make(chan struct{})
			done := make(chan error, 2)
			go func() { <-start; _, err := a.StartRun(ia.ID); done <- err }()
			go func() { <-start; _, err := b.StartRun(ib.ID); done <- err }()
			close(start)
			success := 0
			for range 2 {
				if err := <-done; err == nil {
					success++
				}
			}
			if success != 1 || count() != 1 {
				t.Fatalf("admitted=%d active=%d", success, count())
			}
		})
	}
}

func TestMailboxFilteringPrecedesPagination(t *testing.T) {
	for _, box := range []string{"inbox", "sent"} {
		t.Run(box, func(t *testing.T) {
			f := newAgentFixture(t)
			for i := 0; i < 3; i++ {
				from, to := f.srcID.ParticipantID, f.tgtID.ParticipantID
				if (box == "inbox" && i == 2) || (box == "sent" && i < 2) {
					from, to = to, from
				}
				if _, _, err := f.p.Store.AddMessage(model.Message{FromParticipantID: from, ToParticipantID: to, Text: "hello"}); err != nil {
					t.Fatal(err)
				}
			}
			rec := f.call(t, f.srcID, "GET", "/api/messages?box="+box+"&limit=2", nil)
			var got struct {
				Messages []model.Message `json:"messages"`
				Next     uint64          `json:"next"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 200 || len(got.Messages) != 1 || got.Next != 3 {
				t.Fatalf("page=%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestLiveDeliveryPreventsRetractionAndPreservesEarlyAck(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "transport_error"}[fail], func(t *testing.T) {
			f := newAgentFixture(t)
			f.makeLive(t)
			f.srv.Say = func(_ context.Context, _ model.Intent, _ aopexec.SessionInfo, _ string, _ bool) (string, error) {
				m := f.p.Store.Messages(10)[0]
				if m.Status != model.MessageSending {
					t.Errorf("inflight=%s", m.Status)
				}
				if _, err := f.p.Store.RetractMessage(m.ID, m.FromParticipantID); !errors.Is(err, store.ErrMessageNotQueued) {
					t.Errorf("retract=%v", err)
				}
				ack := f.call(t, f.tgtID, "POST", "/api/messages/"+m.ID+"/ack", nil)
				if ack.Code != 200 {
					t.Errorf("ack=%d", ack.Code)
				}
				if fail {
					return "", errors.New("timeout")
				}
				return "delivered", nil
			}
			rec := f.call(t, f.srcID, "POST", "/api/messages", sendBody(f.tgtID.ParticipantID, "once", "hello"))
			if rec.Code != 202 {
				t.Fatalf("send=%d %s", rec.Code, rec.Body.String())
			}
			m := f.p.Store.Messages(10)[0]
			if m.Status != model.MessageConfirmed || m.AckedAt == nil || m.DeliveredRunID == "" {
				t.Fatalf("receipt=%+v", m)
			}
		})
	}
}

func TestReadyOperatorReceiptSurvivesHandoffAndExplicitRun(t *testing.T) {
	for _, mode := range []string{"handoff", "run"} {
		for _, resolution := range []string{model.OperatorCompleted, model.OperatorUnavailable} {
			t.Run(mode+"/"+resolution, func(t *testing.T) {
				f := newParentFixture(t)
				ex := &blockingExec{}
				f.srv.Exec = ex
				t.Cleanup(func() { f.srv.SetPaused(true); f.srv.CancelAll(); f.srv.WaitIdle(3 * time.Second) })
				req, err := f.p.Store.RegisterOperatorWait(f.ident.IntentID, f.run.ID, model.OperatorWait{Request: "login"})
				if err != nil {
					t.Fatal(err)
				}
				reason := "operator-constraint-preserved"
				if err := f.p.Store.ResolveOperatorWait(f.ident.IntentID, req.ID, reason, resolution); err != nil {
					t.Fatal(err)
				}
				if _, err := f.p.Store.SettleIntentForRun(f.ident.IntentID, f.run.ID, model.RunDone); err != nil {
					t.Fatal(err)
				}
				_, _ = f.p.Store.UpdateRun(f.run.ID, func(r *model.Run) { r.Status = model.RunDone })
				if mode == "handoff" {
					_, h, _, err := f.srv.HandoffIntent(context.Background(), f.ident.IntentID, "", "new session", 0)
					if err != nil {
						t.Fatal(err)
					}
					if h.Wait == nil || h.Wait.Operator.Response != reason {
						t.Fatalf("snapshot=%+v", h)
					}
				} else if _, err := f.srv.StartRun(f.ident.IntentID); err != nil {
					t.Fatal(err)
				}
				waitFor(t, livenessWait, func() bool { return len(ex.requests()) == 1 })
				if !strings.Contains(ex.requests()[0].Prompt, reason) {
					t.Fatal("replacement prompt lost receipt")
				}
				in, _ := f.p.Store.GetIntent(f.ident.IntentID)
				if in.Wait != nil {
					t.Fatal("receipt was not consumed")
				}
			})
		}
	}
}

func TestCommanderDeliveryPreservesReceiptAndCannotBeRetracted(t *testing.T) {
	f := newAgentFixture(t)
	run := f.makeCommanderLive(t)
	f.srv.Say = func(_ context.Context, _ model.Intent, _ aopexec.SessionInfo, _ string, _ bool) (string, error) {
		m := f.p.Store.Messages(10)[0]
		if _, err := f.p.Store.RetractMessage(m.ID, m.FromParticipantID); !errors.Is(err, store.ErrMessageNotQueued) {
			t.Errorf("retract=%v", err)
		}
		ack := f.call(t, collab.Identity{ParticipantID: collab.CommanderID}, "POST", "/api/messages/"+m.ID+"/ack", nil)
		if ack.Code != 200 {
			t.Errorf("ack=%d", ack.Code)
		}
		return "delivered", nil
	}
	rec := f.call(t, f.srcID, "POST", "/api/messages", sendBody(collab.CommanderID, "once", "hello"))
	if rec.Code != 202 {
		t.Fatalf("send=%d %s", rec.Code, rec.Body.String())
	}
	m := f.p.Store.Messages(10)[0]
	if m.Status != model.MessageConfirmed || m.DeliveredRunID != run.ID {
		t.Fatalf("delivery=%+v", m)
	}
}

func TestAdmittedRunCanBeCanceledImmediately(t *testing.T) {
	s, _ := admissionServer(t)
	in, _ := s.Store.CreateIntent(model.Intent{Title: "cancel immediately"})
	runID, err := s.StartRun(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CancelRun(runID); err != nil {
		t.Fatal(err)
	}
	if !s.WaitIdle(3 * time.Second) {
		t.Fatal("admitted run did not finish")
	}
	r, _ := s.Store.GetRun(runID)
	if r.Status != model.RunCanceled || r.CancelCause != model.CancelCauseOperator {
		t.Fatalf("canceled=%+v", r)
	}
}
