package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

func addOperatorMessage(t *testing.T, st *store.Store, text string) model.Message {
	t.Helper()
	msg, created, err := st.AddMessage(model.Message{
		FromParticipantID: collab.OperatorID,
		ToParticipantID:   collab.CommanderID,
		Kind:              "directive",
		Text:              text,
		Durable:           true,
		Status:            model.MessageQueued,
	})
	if err != nil || !created {
		t.Fatalf("add message = %v created=%v", err, created)
	}
	return msg
}

func writeJournalFile(t *testing.T, dataDir, runID string, lines ...any) string {
	t.Helper()
	dir := filepath.Join(dataDir, "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, runID+".jsonl")
	var b strings.Builder
	for _, line := range lines {
		raw, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func markPromptDelivered(t *testing.T, st *store.Store, msgID, runID string) {
	t.Helper()
	if _, err := st.UpdateMessage(msgID, func(m *model.Message) {
		m.Status = model.MessageAccepted
		m.DeliveredRunID = runID
		m.DeliveryID = "prompt:" + runID
		m.DeliveryPath = "prompt"
	}); err != nil {
		t.Fatal(err)
	}
}

func getConsumption(t *testing.T, s *Server, opts ...scopeCallOption) *operatorConsumptionResponse {
	t.Helper()
	w := callOperatorScope(t, s, http.MethodGet, "/api/operator-consumption", nil, opts...)
	if w.Code != http.StatusOK {
		t.Fatalf("consumption = %d %s", w.Code, w.Body.String())
	}
	var out operatorConsumptionResponse
	decodeScopeResponse(t, w, &out)
	return &out
}

// An empty project is explicit about having no data instead of inventing zeros
// from the ledger's timestamps.
func TestOperatorConsumptionEmpty(t *testing.T) {
	s, _, _ := newOperatorScopeServer(t, &blockingExec{})
	out := getConsumption(t, s)
	if out.Unavailable != "no_operator_messages" {
		t.Fatalf("unavailable = %q", out.Unavailable)
	}
	if len(out.Messages) != 0 || out.Counts.Messages != 0 {
		t.Fatalf("messages = %+v counts = %+v", out.Messages, out.Counts)
	}
}

// Delivered and consumed are separate observations: a prompt-carried message is
// consumed only when its id is actually in a commander run's journal, and a
// delivered-but-unreferenced message stays consumed=false (never inferred from
// the delivery time). The endpoint is read-only.
func TestOperatorConsumptionDeliveredVsConsumed(t *testing.T) {
	s, st, dir := newOperatorScopeServer(t, &blockingExec{})

	consumedMsg := addOperatorMessage(t, st, "新增资产 host:a，请纳入")
	unreferencedMsg := addOperatorMessage(t, st, "另一条已投递但没人引用的消息")
	neverMsg := addOperatorMessage(t, st, "还在队列里的消息")

	commanderRun, err := st.AddRun(model.Run{Purpose: "commander"})
	if err != nil {
		t.Fatal(err)
	}
	journal := writeJournalFile(t, dir, commanderRun.ID,
		map[string]any{"kind": "input", "at": time.Now(), "prompt_section": "commander_round", "text": "## 未读消息\n- [消息] from=operator message_id=" + consumedMsg.ID + " kind=directive\n新增资产"},
		map[string]any{"kind": "turn_end", "at": time.Now(), "stop_reason": "completed"},
	)
	// A second commander run whose input does not mention the second message.
	otherRun, err := st.AddRun(model.Run{Purpose: "commander"})
	if err != nil {
		t.Fatal(err)
	}
	writeJournalFile(t, dir, otherRun.ID,
		map[string]any{"kind": "input", "at": time.Now(), "text": "本轮没有携带操作员消息"},
	)
	markPromptDelivered(t, st, consumedMsg.ID, commanderRun.ID)
	markPromptDelivered(t, st, unreferencedMsg.ID, otherRun.ID)

	hashBefore := st.ContentHash()
	journalBefore, _ := os.ReadFile(journal)

	out := getConsumption(t, s)
	if out.Unavailable != "" {
		t.Fatalf("unavailable = %q", out.Unavailable)
	}
	byID := map[string]operatorConsumptionMessage{}
	for _, m := range out.Messages {
		byID[m.ID] = m
	}
	if len(byID) != 3 {
		t.Fatalf("messages = %+v", out.Messages)
	}

	got := byID[consumedMsg.ID]
	if !got.Delivered || got.Delivery.DeliveryPath != "prompt" || got.Delivery.DeliveredRunID != commanderRun.ID {
		t.Fatalf("consumed msg delivery = %+v", got.Delivery)
	}
	if !got.Consumed || got.ConsumedByRunID != commanderRun.ID || got.ConsumedByRunPurpose != "commander" {
		t.Fatalf("consumed msg = %+v", got)
	}
	if got.Evidence != "journal_input" {
		t.Fatalf("evidence = %q", got.Evidence)
	}
	if got.ConsumedAt == nil || got.LatencyMS == nil || *got.LatencyMS < 0 {
		t.Fatalf("consumption time/latency = %v/%v", got.ConsumedAt, got.LatencyMS)
	}
	if got.ConsumedAt.Before(got.CreatedAt) {
		t.Fatalf("consumed_at %v before created %v", *got.ConsumedAt, got.CreatedAt)
	}

	unreferenced := byID[unreferencedMsg.ID]
	if !unreferenced.Delivered || unreferenced.Consumed {
		t.Fatalf("unreferenced msg = %+v (must be delivered but not consumed)", unreferenced)
	}
	if unreferenced.Evidence != "no_run_input" {
		t.Fatalf("unreferenced evidence = %q", unreferenced.Evidence)
	}
	if unreferenced.LatencyMS != nil || unreferenced.ConsumedAt != nil {
		t.Fatalf("unreferenced inference leaked: %+v", unreferenced)
	}

	queued := byID[neverMsg.ID]
	if queued.Delivered || queued.Consumed || queued.Delivery.State != model.MessageQueued {
		t.Fatalf("queued msg = %+v", queued)
	}

	if out.Counts.Messages != 3 || out.Counts.Delivered != 2 || out.Counts.Consumed != 1 {
		t.Fatalf("counts = %+v", out.Counts)
	}
	// Read-only: no ledger write and no journal rewrite.
	if after := st.ContentHash(); after != hashBefore {
		t.Fatalf("consumption changed the ledger: %q -> %q", hashBefore, after)
	}
	journalAfter, _ := os.ReadFile(journal)
	if string(journalAfter) != string(journalBefore) {
		t.Fatal("consumption rewrote a run journal")
	}
	if _, err := os.Stat(store.OperatorScopePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("consumption created the scope file: %v", err)
	}
}

// A live (IOA) delivery names its run explicitly; the collab_message event is the
// identity evidence and the recorded delivery time is the consumption time.
func TestOperatorConsumptionLiveDelivery(t *testing.T) {
	s, st, dir := newOperatorScopeServer(t, &blockingExec{})
	liveRun, err := st.AddRun(model.Run{Purpose: "commander"})
	if err != nil {
		t.Fatal(err)
	}
	msg := addOperatorMessage(t, st, "运行中插入的操作员提示")
	writeJournalFile(t, dir, liveRun.ID,
		map[string]any{"kind": "collab_message", "at": time.Now(), "message_id": msg.ID, "from": collab.OperatorID, "to": collab.CommanderID, "direction": "in", "delivery": "accepted"},
	)
	if _, err := st.UpdateMessage(msg.ID, func(m *model.Message) {
		m.Status = model.MessageAccepted
		m.DeliveredRunID = liveRun.ID
		m.DeliveryID = "ioa-1"
		m.DeliveryPath = "ioa"
	}); err != nil {
		t.Fatal(err)
	}
	out := getConsumption(t, s)
	got := out.Messages[0]
	if !got.Consumed || got.Evidence != "journal_collab_message" || got.ConsumedByRunID != liveRun.ID {
		t.Fatalf("live consumption = %+v", got)
	}
	if got.ConsumedAt == nil || got.ConsumedAt.Before(got.CreatedAt) {
		t.Fatalf("consumed_at = %v, created = %v", got.ConsumedAt, got.CreatedAt)
	}
}

// Like the rest of the operator scope surface, the read-only observation is
// absent (404) for harness sessions and the commander actor claim.
func TestOperatorConsumptionOperatorOnly(t *testing.T) {
	s, _, _ := newOperatorScopeServer(t, &blockingExec{})
	for name, opt := range map[string]scopeCallOption{
		"commander identity":    withCommanderIdentity,
		"executor identity":     withExecutorIdentity,
		"agent surface":         withAgentSurface,
		"commander actor claim": withCommanderActorClaim,
	} {
		t.Run(name, func(t *testing.T) {
			w := callOperatorScope(t, s, http.MethodGet, "/api/operator-consumption", nil, opt)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d %s, want 404", w.Code, w.Body.String())
			}
		})
	}
}
