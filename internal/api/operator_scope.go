package api

// Operator scope: the operator-only surface over <projectDir>/operator-scope.jsonl.
//
// Two recorded decisions share one append-only file (internal/store/operator_scope.go):
//
//   - authorization: the operator records a widened scope decision;
//   - stop: the operator explicitly names the directions and/or entities to stop.
//
// A stop is applied to the directions the operator named, using the existing
// cancel semantics (CancelIntent: Hold first, then cancel the live run). The
// program never derives the affected directions from interest_entity_refs,
// titles, or the stop's prose: only the explicit intent_ids list cancels
// anything. entity_refs are a read-only annotation rule for facts recorded after
// the stop (board and graph projections mark them late_after_stop).
//
// The endpoints are operator-only. A harness session (commander or executor)
// calling the project API is answered 404, exactly like the other operator-only
// surfaces: a 403 would confirm the surface exists. Reads are also operator-only
// because the file is the operator's private scope record, not battlefield data.
//
// Known limitation (documented for the release): this is not per-request
// enforcement. The same OS account can edit operator-scope.jsonl or call the
// API with the console token; the file is durable evidence of the operator's
// decisions, not an ACL.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kksty/saker/internal/board"
	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

const operatorScopeMaxBodyBytes = 256 << 10

// operatorScopeOperatorOnly is the one gate for all three operator-scope
// surfaces (GET/POST /api/operator-scope, GET /api/operator-consumption). It
// mirrors operatorInstructionWrite: a resolved participant identity or a
// harness-session marker means "a model is calling", and a commander actor claim
// means the session wrote through the project credential. Both are absent for a
// real console operator request.
func operatorScopeOperatorOnly(w http.ResponseWriter, r *http.Request) bool {
	if _, participant := identityOf(r); participant || harnessSession(r) || actorOf(r) != collab.OperatorID {
		http.NotFound(w, r)
		return false
	}
	return true
}

// projectDir is the directory the bypass file lives in. Server.DataDir is the
// project root in production (hub attaches New with p.Dir); Project.Dir is
// preferred when a Project is attached so a test server and a real project agree.
func (s *Server) projectDir() string {
	if s.Project != nil && strings.TrimSpace(s.Project.Dir) != "" {
		return s.Project.Dir
	}
	return s.DataDir
}

func (s *Server) readOperatorScope() (store.OperatorScope, error) {
	return store.ReadOperatorScope(s.projectDir())
}

// ---- GET /api/operator-scope ----

type operatorScopeIntentView struct {
	ID    string `json:"id"`
	Exist bool   `json:"exists"`
	// Status/Hold are the ledger's current answer for a direction the operator
	// listed; both are derived at read time, not stored in the file.
	Status       string `json:"status,omitempty"`
	Hold         bool   `json:"hold,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	CancelReason string `json:"cancel_reason,omitempty"`
}

type operatorScopeEntryView struct {
	store.OperatorScopeEntry
	// StoppedIntents is present only for kind=stop: what the ledger says about
	// each explicitly listed direction now.
	StoppedIntents []operatorScopeIntentView `json:"stopped_intents,omitempty"`
	// LateFactIDs are facts whose entity_ref is listed in entity_refs and whose
	// recorded time is after this stop's at. It is a timing annotation, not a
	// judgement about the fact.
	LateFactIDs []string `json:"late_fact_ids,omitempty"`
}

type operatorScopeLateFact struct {
	FactID    string    `json:"fact_id"`
	EntityRef string    `json:"entity_ref"`
	CreatedAt time.Time `json:"created_at"`
	StopID    string    `json:"stop_id"`
	StopAt    time.Time `json:"stop_at"`
}

type operatorScopeCounts struct {
	Total          int `json:"total"`
	Stops          int `json:"stops"`
	Authorizations int `json:"authorizations"`
	LateFacts      int `json:"late_facts"`
}

type operatorScopeReadView struct {
	Version int    `json:"version"`
	File    string `json:"file"`
	// Entries is the complete record in file order; stops/authorizations are
	// convenience subsets of the same rows, never a second source.
	Entries        []operatorScopeEntryView     `json:"entries"`
	Stops          []operatorScopeEntryView     `json:"stops"`
	Authorizations []operatorScopeEntryView     `json:"authorizations"`
	LateFacts      []operatorScopeLateFact      `json:"late_facts"`
	BadLines       []store.OperatorScopeBadLine `json:"bad_lines,omitempty"`
	TruncatedTail  bool                         `json:"truncated_tail,omitempty"`
	Counts         operatorScopeCounts          `json:"counts"`
	// Unavailable is empty when the file exists (entries may legitimately be
	// empty); "no_operator_scope_file" says the project never recorded one.
	Unavailable string `json:"unavailable,omitempty"`
	Note        string `json:"note"`
}

const operatorScopeNote = "operator-scope.jsonl 为 append-only 旁路文件：条目只追加、不重写；at 是操作员记录的决定时间。stops[].stopped_intents 与 late_fact_ids 由账本当前状态派生，不是第二份事实；bad_lines 表示中间行无法解析（尾部半行记为 truncated_tail 并忽略）。"

func (s *Server) operatorScopeView(scope store.OperatorScope) operatorScopeReadView {
	db := s.Store.Snapshot()
	view := operatorScopeReadView{
		Version:        store.OperatorScopeVersion,
		File:           store.OperatorScopeFile,
		Entries:        []operatorScopeEntryView{},
		Stops:          []operatorScopeEntryView{},
		Authorizations: []operatorScopeEntryView{},
		LateFacts:      []operatorScopeLateFact{},
		BadLines:       scope.BadLines,
		TruncatedTail:  scope.TruncatedTail,
		Note:           operatorScopeNote,
	}
	if !operatorScopeFileExists(s.projectDir()) {
		view.Unavailable = "no_operator_scope_file"
	}
	for _, entry := range scope.Entries {
		item := operatorScopeEntryView{OperatorScopeEntry: entry}
		if entry.Kind == store.OperatorScopeKindStop {
			for _, id := range entry.IntentIDs {
				item.StoppedIntents = append(item.StoppedIntents, s.operatorScopeIntentView(id))
			}
			for _, fact := range db.Facts {
				if !entry.MatchesLateFact(fact) {
					continue
				}
				item.LateFactIDs = append(item.LateFactIDs, fact.ID)
				view.LateFacts = append(view.LateFacts, operatorScopeLateFact{
					FactID: fact.ID, EntityRef: fact.EntityRef, CreatedAt: fact.CreatedAt,
					StopID: entry.ID, StopAt: entry.At,
				})
			}
			view.Stops = append(view.Stops, item)
		} else {
			view.Authorizations = append(view.Authorizations, item)
		}
		view.Entries = append(view.Entries, item)
	}
	// Newest first: a console must show today's decision before last week's.
	sort.SliceStable(view.LateFacts, func(i, j int) bool {
		if !view.LateFacts[i].CreatedAt.Equal(view.LateFacts[j].CreatedAt) {
			return view.LateFacts[i].CreatedAt.After(view.LateFacts[j].CreatedAt)
		}
		return view.LateFacts[i].FactID < view.LateFacts[j].FactID
	})
	view.Counts = operatorScopeCounts{
		Total:          len(view.Entries),
		Stops:          len(view.Stops),
		Authorizations: len(view.Authorizations),
		LateFacts:      len(view.LateFacts),
	}
	return view
}

func (s *Server) operatorScopeIntentView(id string) operatorScopeIntentView {
	out := operatorScopeIntentView{ID: id}
	intent, ok := s.Store.GetIntent(id)
	if !ok {
		return out
	}
	out.Exist = true
	out.Status, out.Hold, out.RunID = intent.Status, intent.Hold, intent.RunID
	out.CancelReason = s.runCancelReason(intent.RunID)
	return out
}

func (s *Server) runCancelReason(runID string) string {
	if strings.TrimSpace(runID) == "" {
		return ""
	}
	if run, ok := s.Store.GetRun(runID); ok {
		return run.ExitReason
	}
	return ""
}

func (s *Server) operatorScopeList(w http.ResponseWriter, r *http.Request) {
	if !operatorScopeOperatorOnly(w, r) {
		return
	}
	scope, err := s.readOperatorScope()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.operatorScopeView(scope))
}

// ---- POST /api/operator-scope ----

type operatorScopeCreateRequest struct {
	Kind       string   `json:"kind"`
	Summary    string   `json:"summary"`
	IntentIDs  []string `json:"intent_ids"`
	EntityRefs []string `json:"entity_refs"`
	Note       string   `json:"note"`
	// ID lets a retry name the same entry explicitly. IdempotencyKey derives a
	// stable id from the key, so a double-clicked composer produces one row.
	ID             string `json:"id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type operatorScopeStopOutcome struct {
	ID     string `json:"id"`
	Result string `json:"result"` // held_run_canceled|held|already_held|terminal|not_found|not_canceled
	Status string `json:"status,omitempty"`
	Hold   bool   `json:"hold,omitempty"`
	RunID  string `json:"run_id,omitempty"`
	// Reason is the cancel reason handed to the existing cancel semantics; it
	// always references the stop id, which is what makes the hold traceable.
	Reason       string `json:"reason,omitempty"`
	CancelReason string `json:"cancel_reason,omitempty"`
	Error        string `json:"error,omitempty"`
}

type operatorScopeStopApplication struct {
	StopID         string                     `json:"stop_id"`
	Intents        []operatorScopeStopOutcome `json:"intents"`
	PartialFailure bool                       `json:"partial_failure,omitempty"`
	Note           string                     `json:"note"`
}

type operatorScopeCreateResult struct {
	Entry    store.OperatorScopeEntry      `json:"entry"`
	Created  bool                          `json:"created"`
	Replayed bool                          `json:"replayed,omitempty"`
	Applied  *operatorScopeStopApplication `json:"applied,omitempty"`
	Note     string                        `json:"note"`
}

const operatorScopeCreateNote = "条目已 append 到 operator-scope.jsonl。stop 只取消 intent_ids 中显式列出的意图；entity_refs 不做匹配、不取消任何意图，只用于迟到事实标注。"

func (s *Server) operatorScopeCreate(w http.ResponseWriter, r *http.Request) {
	if !operatorScopeOperatorOnly(w, r) {
		return
	}
	var req operatorScopeCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, operatorScopeMaxBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "操作员范围请求不是合法 JSON", "code": "operator_scope_invalid"})
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		if key := strings.TrimSpace(req.IdempotencyKey); key != "" {
			sum := sha256.Sum256([]byte("operator-scope:" + key))
			id = "os-" + hex.EncodeToString(sum[:8])
		}
	}
	entry, created, err := store.AppendOperatorScope(s.projectDir(), store.OperatorScopeEntry{
		V:          store.OperatorScopeVersion,
		ID:         id,
		Kind:       strings.ToLower(strings.TrimSpace(req.Kind)),
		At:         time.Now(),
		Actor:      collab.OperatorID,
		Summary:    req.Summary,
		IntentIDs:  req.IntentIDs,
		EntityRefs: req.EntityRefs,
		Note:       req.Note,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOperatorScopeConflict):
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "operator_scope_conflict"})
		case errors.Is(err, store.ErrOperatorScopeInvalid):
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "code": "operator_scope_invalid"})
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	result := operatorScopeCreateResult{Entry: entry, Created: created, Replayed: !created, Note: operatorScopeCreateNote}
	if entry.Kind == store.OperatorScopeKindStop {
		// Append first, then apply: a crash between the two leaves a durable
		// stop the console can see and replay (idempotent), never a cancelled
		// direction with no written reason.
		result.Applied = s.applyOperatorScopeStop(entry)
	}
	s.recordAction("operator_scope", entry.ID, entry.Kind+"："+entry.Summary)
	code := http.StatusCreated
	if !created {
		code = http.StatusOK
	}
	writeJSON(w, code, result)
}

// applyOperatorScopeStop cancels exactly the directions the operator listed,
// through the existing CancelIntent semantics (Hold first, then the live run is
// cancelled with a reason that references the stop id). It is idempotent: a
// replayed request finds the directions already held and does not cancel twice.
// entity_refs never cause a cancellation.
func (s *Server) applyOperatorScopeStop(entry store.OperatorScopeEntry) *operatorScopeStopApplication {
	app := &operatorScopeStopApplication{
		StopID:  entry.ID,
		Intents: []operatorScopeStopOutcome{},
		Note:    "只处理 intent_ids 中显式列出的意图；未列出意图不受影响。terminal/not_found 表示停止记录已保存，但不是本次取消对象。",
	}
	reason := "操作员停测 " + entry.ID + "：" + entry.Summary
	for _, id := range entry.IntentIDs {
		outcome := operatorScopeStopOutcome{ID: id, Reason: reason}
		intent, ok := s.Store.GetIntent(id)
		if !ok {
			outcome.Result = "not_found"
			outcome.Error = "意图不存在；停测记录已保存"
			app.Intents = append(app.Intents, outcome)
			continue
		}
		if intent.Hold {
			outcome.Result = "already_held"
			outcome.Status, outcome.Hold, outcome.RunID = intent.Status, true, intent.RunID
			outcome.CancelReason = s.runCancelReason(intent.RunID)
			app.Intents = append(app.Intents, outcome)
			continue
		}
		switch intent.Status {
		case model.StatusDone, model.StatusParked, model.StatusFailed, model.StatusReviewFailed:
			outcome.Result = "terminal"
			outcome.Status = intent.Status
			outcome.Error = "意图已结束（" + intent.Status + "），不通过 cancel 改写终态；停测记录仍保留"
			app.Intents = append(app.Intents, outcome)
			continue
		}
		runID, err := s.CancelIntentBy(id, reason, model.HoldActorOperator)
		current, _ := s.Store.GetIntent(id)
		outcome.Status, outcome.Hold = current.Status, current.Hold
		switch {
		case err != nil:
			outcome.Result = "not_canceled"
			outcome.Error = err.Error()
			app.PartialFailure = true
		case runID != "":
			outcome.Result = "held_run_canceled"
			outcome.RunID = runID
		default:
			outcome.Result = "held"
		}
		if outcome.RunID != "" {
			outcome.CancelReason = s.runCancelReason(outcome.RunID)
		}
		app.Intents = append(app.Intents, outcome)
	}
	return app
}

// ---- read-only projections on board / graph ----

// jsonCaptureWriter lets the existing board/graph handlers render their JSON
// untouched, then adds the operator-scope fields at the JSON layer. This keeps
// the large handlers unmodified (the operator-scope write set is new files) and
// means a harness session can never be served the operator-only fields: the
// wrappers below delegate directly in that case.
type jsonCaptureWriter struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func newJSONCaptureWriter() *jsonCaptureWriter {
	return &jsonCaptureWriter{header: http.Header{}}
}

func (c *jsonCaptureWriter) Header() http.Header { return c.header }

func (c *jsonCaptureWriter) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *jsonCaptureWriter) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.body.Write(b)
}

func (c *jsonCaptureWriter) flushTo(w http.ResponseWriter) {
	for key, values := range c.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	code := c.code
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	_, _ = w.Write(c.body.Bytes())
}

// getBoardWithOperatorScope is the console /api/board entry. Harness sessions get
// the plain board (operator scope is operator-side data); the console gets the
// same board plus operator_scope and late_after_stop.
func (s *Server) getBoardWithOperatorScope(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		s.getBoard(w, r)
		return
	}
	capture := newJSONCaptureWriter()
	s.getBoard(capture, r)
	if capture.code != 0 && capture.code != http.StatusOK || capture.body.Len() == 0 {
		capture.flushTo(w)
		return
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(capture.body.Bytes(), &body); err != nil {
		capture.flushTo(w)
		return
	}
	scope, err := s.readOperatorScope()
	if err != nil {
		// The board must still render; the failure is explicit, not silent.
		body["operator_scope"] = mustJSONMarshal(map[string]any{"error": err.Error(), "entries": []any{}})
		body["late_after_stop"] = mustJSONMarshal(map[string]board.LateAfterStop{})
	} else {
		body["operator_scope"] = mustJSONMarshal(s.operatorScopeView(scope))
		if len(board.OperatorScopeStops(scope.Entries)) == 0 {
			// Nothing can be late after a stop that does not exist. Skipping the
			// snapshot here matters: this is the console's 6-second board poll,
			// and cloning the whole ledger to annotate zero rows was pure cost.
			body["late_after_stop"] = mustJSONMarshal(map[string]board.LateAfterStop{})
		} else {
			db := s.Store.Snapshot()
			body["late_after_stop"] = mustJSONMarshal(board.LateFactsByFact(db.Facts, scope.Entries))
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	code := capture.code
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// graphQueryWithOperatorScope is the console /api/graph entry. The agent mux
// keeps mounting the original graphQuery, so an executor/commander session never
// sees the operator-side annotation, and this wrapper does not duplicate the
// graph query contract: it renders the same handler and annotates its JSON.
func (s *Server) graphQueryWithOperatorScope(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		s.graphQuery(w, r)
		return
	}
	capture := newJSONCaptureWriter()
	s.graphQuery(capture, r)
	if (capture.code != 0 && capture.code != http.StatusOK) || capture.body.Len() == 0 {
		capture.flushTo(w)
		return
	}
	var body struct {
		Facts []model.Fact `json:"facts"`
	}
	if err := json.Unmarshal(capture.body.Bytes(), &body); err != nil {
		capture.flushTo(w)
		return
	}
	scope, err := s.readOperatorScope()
	if err != nil {
		capture.flushTo(w)
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(capture.body.Bytes(), &raw); err != nil {
		capture.flushTo(w)
		return
	}
	if len(board.OperatorScopeStops(scope.Entries)) == 0 {
		raw["late_after_stop"] = mustJSONMarshal(map[string]board.LateAfterStop{})
	} else {
		raw["late_after_stop"] = mustJSONMarshal(board.LateFactsByFact(body.Facts, scope.Entries))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	code := capture.code
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(raw)
}

func mustJSONMarshal(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// operatorScopeFileExists reports whether the scope file exists, letting the GET
// view distinguish "file exists, no entries" from "project never recorded one"
// without inventing data.
func operatorScopeFileExists(projectDir string) bool {
	_, err := os.Stat(store.OperatorScopePath(projectDir))
	return err == nil
}
