package api

// Operator instruction consumption: the read-only answer to "my message says
// delivered — did anything downstream actually take it as input?".
//
// Delivered and consumed are different facts and are reported separately:
//
//   - delivered comes from the message row's existing delivery record
//     (status accepted/confirmed, delivery_id, delivered_run_id, delivery_path);
//   - consumed means the message's identity appears in the run journal of a run
//     that could have read it: for a prompt-carried row, the run the prompt was
//     built for started after the message was created; for a live (IOA) row, the
//     row itself names that run. The match is a byte/identity match on the
//     message id inside the run's own event stream, never a text/semantic
//     classification of the message body.
//
// Consumption is still not a read receipt: the journal proves the id was in the
// run's input/delivery record, not that the model read or acted on it. The
// endpoint is read-only (snapshot + journal file opens, no writes) and
// operator-only. Journal files that are missing or unreadable are counted, not
// guessed around; a message with no evidence says so instead of inferring from
// timestamps.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
)

// operatorConsumptionMaxJournalBytes bounds how much of one run journal is read.
// The message id appears in the input/inbox section near the start of the journal,
// so a truncated scan is still useful; the count is reported as truncated so a
// false "not consumed" from a very large journal is visible, not inferred.
const operatorConsumptionMaxJournalBytes = 16 << 20

type operatorConsumptionDelivery struct {
	State          string     `json:"state"`
	DeliveryID     string     `json:"delivery_id,omitempty"`
	DeliveredRunID string     `json:"delivered_run_id,omitempty"`
	DeliveryPath   string     `json:"delivery_path,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	Error          string     `json:"error,omitempty"`
}

type operatorConsumptionMessage struct {
	ID        string    `json:"id"`
	Seq       uint64    `json:"seq"`
	CreatedAt time.Time `json:"created_at"`
	To        string    `json:"to_participant_id"`
	Kind      string    `json:"kind,omitempty"`
	Status    string    `json:"status"`
	// Delivered is the existing delivery record's answer. A "sending" or
	// "unknown" row is not delivered here: the bytes may never have arrived.
	Delivered   bool                        `json:"delivered"`
	Delivery    operatorConsumptionDelivery `json:"delivery"`
	ReadAckedAt *time.Time                  `json:"read_acked_at,omitempty"`
	// Consumed is true only when the message identity is found in a run journal
	// that could have read it. Evidence names what matched; no_run_input means
	// the check ran and found nothing (never inferred from time or delivery).
	Consumed             bool       `json:"consumed"`
	ConsumedByRunID      string     `json:"consumed_by_run_id,omitempty"`
	ConsumedByRunPurpose string     `json:"consumed_by_run_purpose,omitempty"`
	ConsumedAt           *time.Time `json:"consumed_at,omitempty"`
	LatencyMS            *int64     `json:"latency_ms,omitempty"`
	Evidence             string     `json:"evidence"`
}

type operatorConsumptionCounts struct {
	Messages  int `json:"messages"`
	Delivered int `json:"delivered"`
	Consumed  int `json:"consumed"`
}

type operatorConsumptionResponse struct {
	Version     int                          `json:"version"`
	GeneratedAt time.Time                    `json:"generated_at"`
	Messages    []operatorConsumptionMessage `json:"messages"`
	Counts      operatorConsumptionCounts    `json:"counts"`
	RunsScanned int                          `json:"runs_scanned"`
	RunsMissing int                          `json:"runs_missing_journal"`
	// JournalsTruncated counts journals where the bounded scan stopped before
	// the end: a consumed=false there is "not found in the scanned prefix", and
	// the count makes that limitation explicit instead of looking like proof.
	JournalsTruncated int    `json:"journals_truncated,omitempty"`
	Unavailable       string `json:"unavailable,omitempty"`
	Note              string `json:"note"`
}

const operatorConsumptionNote = "delivered 来自消息行的既有投递记录（accepted/confirmed）；consumed 表示消息 id 出现在承载该消息的 run 日志/输入记录中（prompt 路径：run 在消息创建后启动；ioa 路径：消息行指向该 live run）。二者都不证明模型已读或已执行；没有证据时 consumed=false 且 evidence=no_run_input，不按时间推断。"

// consumptionReference is one journal match: which run carried the id and what
// kind of record it was found in.
type consumptionReference struct {
	run      model.Run
	evidence string
}

func (s *Server) operatorConsumption(w http.ResponseWriter, r *http.Request) {
	if !operatorScopeOperatorOnly(w, r) {
		return
	}
	db := s.Store.Snapshot()
	messages := make([]model.Message, 0, len(db.Messages))
	for _, m := range db.Messages {
		if m.FromParticipantID == collab.OperatorID {
			messages = append(messages, m)
		}
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].Seq < messages[j].Seq })
	out := operatorConsumptionResponse{
		Version:     1,
		GeneratedAt: time.Now(),
		Messages:    []operatorConsumptionMessage{},
		Note:        operatorConsumptionNote,
	}
	if len(messages) == 0 {
		out.Unavailable = "no_operator_messages"
		writeJSON(w, http.StatusOK, out)
		return
	}
	refs, scanned, missing, truncated := s.operatorConsumptionReferences(messages, db.Runs)
	out.RunsScanned, out.RunsMissing, out.JournalsTruncated = scanned, missing, truncated
	for _, m := range messages {
		item := operatorConsumptionMessage{
			ID:        m.ID,
			Seq:       m.Seq,
			CreatedAt: m.CreatedAt,
			To:        m.ToParticipantID,
			Kind:      m.Kind,
			Status:    m.Status,
			Delivery: operatorConsumptionDelivery{
				State:          m.Status,
				DeliveryID:     m.DeliveryID,
				DeliveredRunID: m.DeliveredRunID,
				DeliveryPath:   m.DeliveryPath,
				Error:          m.Error,
			},
		}
		if m.Status == model.MessageAccepted || m.Status == model.MessageConfirmed {
			item.Delivered = true
		}
		if m.DeliveredRunID != "" || m.DeliveryID != "" {
			at := m.UpdatedAt
			item.Delivery.DeliveredAt = &at
		}
		if m.AckedAt != nil {
			at := *m.AckedAt
			item.ReadAckedAt = &at
		}
		item.Evidence = "no_run_input"
		if ref, ok := s.pickConsumptionReference(m, refs[m.ID]); ok {
			item.Consumed = true
			item.Evidence = ref.evidence
			item.ConsumedByRunID = ref.run.ID
			item.ConsumedByRunPurpose = ref.run.Purpose
			at := consumptionAt(m, ref.run)
			item.ConsumedAt = &at
			latency := at.Sub(m.CreatedAt)
			if latency < 0 {
				latency = 0
			}
			ms := latency.Milliseconds()
			item.LatencyMS = &ms
		}
		if item.Delivered {
			out.Counts.Delivered++
		}
		if item.Consumed {
			out.Counts.Consumed++
		}
		out.Messages = append(out.Messages, item)
	}
	out.Counts.Messages = len(out.Messages)
	writeJSON(w, http.StatusOK, out)
}

// pickConsumptionReference enforces the "could have read it" rule for one
// message. A reference only counts when the run is the row's delivery run or the
// run started after the message was created; anything else is a coincidence of a
// recycled/short id and is ignored.
func (s *Server) pickConsumptionReference(m model.Message, refs []consumptionReference) (consumptionReference, bool) {
	for _, ref := range refs {
		if ref.run.ID == m.DeliveredRunID {
			return ref, true
		}
		if !ref.run.StartedAt.IsZero() && !ref.run.StartedAt.Before(m.CreatedAt) {
			return ref, true
		}
	}
	return consumptionReference{}, false
}

// consumptionAt is the honest timestamp for "when the run input took this
// message": the carrying run's start for a prompt row, the delivery completion
// for a live IOA row.
func consumptionAt(m model.Message, run model.Run) time.Time {
	at := run.StartedAt
	if run.ID == m.DeliveredRunID && m.DeliveryPath == "ioa" {
		at = m.UpdatedAt
	}
	if at.IsZero() || at.Before(m.CreatedAt) {
		at = m.CreatedAt
	}
	return at
}

// operatorConsumptionReferences scans the journals of runs that could have read
// one of the messages and returns message id -> matches. The journal is the run's
// own input/delivery record (journal.go): a prompt row appears as the "input"
// event's text and/or the per-message inbox claim; a live IOA row appears as a
// collab_message event carrying message_id.
func (s *Server) operatorConsumptionReferences(messages []model.Message, runs []model.Run) (map[string][]consumptionReference, int, int, int) {
	ids := make([]string, 0, len(messages))
	deliveredRuns := map[string]bool{}
	earliest := messages[0].CreatedAt
	for _, m := range messages {
		ids = append(ids, m.ID)
		if m.DeliveredRunID != "" {
			deliveredRuns[m.DeliveredRunID] = true
		}
		if m.CreatedAt.Before(earliest) {
			earliest = m.CreatedAt
		}
	}
	candidates := make([]model.Run, 0, len(runs))
	for _, run := range runs {
		if deliveredRuns[run.ID] || (!run.StartedAt.IsZero() && !run.StartedAt.Before(earliest)) {
			candidates = append(candidates, run)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].StartedAt.Before(candidates[j].StartedAt) })
	refs := map[string][]consumptionReference{}
	scanned, missing, truncated := 0, 0, 0
	for _, run := range candidates {
		found, wasTruncated, err := scanOperatorConsumptionJournal(s.journalPath(run), ids)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				missing++
			}
			continue
		}
		scanned++
		if wasTruncated {
			truncated++
		}
		for id, evidence := range found {
			refs[id] = append(refs[id], consumptionReference{run: run, evidence: evidence})
		}
	}
	return refs, scanned, missing, truncated
}

// journalPath is the same path dispatch/commander wrote: the row's LogPath when
// present (older rows), otherwise the data dir's runs/<id>.jsonl convention.
func (s *Server) journalPath(run model.Run) string {
	if strings.TrimSpace(run.LogPath) != "" {
		return run.LogPath
	}
	return filepath.Join(s.DataDir, "runs", run.ID+".jsonl")
}

// scanOperatorConsumptionJournal reads one journal and reports which of the given
// ids appear. A structured match (the journal's own message_id field) is stronger
// than the id appearing inside a rendered input text; both are identity matches,
// never semantic ones.
func scanOperatorConsumptionJournal(path string, ids []string) (map[string]string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	found := map[string]string{}
	wanted := make([]string, len(ids))
	copy(wanted, ids)
	truncated := false
	r := bufio.NewReaderSize(f, 64<<10)
	var total int64
	for {
		line, readErr := r.ReadBytes('\n')
		total += int64(len(line))
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			var ev struct {
				Kind      string `json:"kind"`
				MessageID string `json:"message_id"`
				Text      string `json:"text"`
			}
			parsed := json.Unmarshal(line, &ev) == nil
			for _, id := range wanted {
				if id == "" || found[id] != "" {
					continue
				}
				evidence := ""
				switch {
				case parsed && ev.MessageID == id:
					if ev.Kind == "collab_message" {
						evidence = "journal_collab_message"
					} else {
						evidence = "journal_message_id"
					}
				case parsed && ev.Text != "" && strings.Contains(ev.Text, id):
					evidence = "journal_input"
				case !parsed && bytes.Contains(line, []byte(id)):
					evidence = "journal_raw"
				}
				if evidence != "" {
					found[id] = evidence
				}
			}
		}
		if readErr != nil {
			break // EOF or an unreadable remainder: keep what was matched so far
		}
		if total > operatorConsumptionMaxJournalBytes {
			truncated = true
			break
		}
	}
	return found, truncated, nil
}
