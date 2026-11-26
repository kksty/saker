package api

import (
	"context"
	"testing"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
)

type failedCommanderInputExec struct{}

func (failedCommanderInputExec) Name() string { return "failed-commander-input" }
func (failedCommanderInputExec) Run(context.Context, executor.Request, func(executor.Event)) (executor.Result, error) {
	return executor.Result{ExitCode: 1, IsError: true, StopReason: "error", Output: "connection unavailable"}, nil
}

func TestFailedCommanderTransportRequeuesItsPromptInput(t *testing.T) {
	s, _, st := newCommanderServer(t)
	s.Exec = failedCommanderInputExec{}
	m, _, err := st.AddMessage(model.Message{FromParticipantID: collab.OperatorID, ToParticipantID: collab.CommanderID, Text: "explicit global request", Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.StartCommander(context.Background(), withBoardAnchor("round"), false); err == nil {
		t.Fatal("fixture did not fail")
	}
	got, _ := st.MessageByID(m.ID)
	if got.Status != model.MessageQueued || got.Redeliveries != 1 || got.EventID != m.EventID {
		t.Fatalf("failed API lost or duplicated request identity: %+v", got)
	}
}
