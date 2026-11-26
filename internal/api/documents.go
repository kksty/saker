package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/model"
	"github.com/kksty/saker/internal/store"
)

// documentView is one of the three project documents as the console reads it.
// Revision 0 means "as loaded, never edited through this API" — the text still
// comes from the project (scope, constraint) or the goal object, so the console
// always has something to show and a first save is unambiguous.
type documentView struct {
	Text      string    `json:"text"`
	Revision  uint64    `json:"revision"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	GoalID    string    `json:"goal_id,omitempty"`
}

// documentRevision is one row of a document's history. The field names are the
// console's: it formats history rows with the same revisionLine as the document
// card (updated_at/updated_by), and the acceptance fixture serves that shape.
// They used to be "at"/"by", which the console could not read — every history row
// therefore showed "(未记录)" for both the time and the author.
type documentRevision struct {
	Revision  uint64    `json:"revision"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	Text      string    `json:"text"`
	Diff      string    `json:"diff,omitempty"`
}

// currentGoalDocument resolves the one editable goal: the earliest open goal by
// creation time. More than one open goal is possible (the commander may append),
// and the console says so without editing a different one here.
func (s *Server) currentGoalDocument() (model.Goal, bool) {
	g, _, ok := s.Store.CurrentGoal()
	return g, ok
}

func (s *Server) documentsView() map[string]documentView {
	out := map[string]documentView{}
	fallback := func(kind, text string) documentView {
		if d, ok := s.Store.Document(kind); ok {
			return documentView{Text: d.Text, Revision: d.Revision, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy}
		}
		return documentView{Text: text}
	}
	out[store.DocumentScope] = fallback(store.DocumentScope, s.scopeText())
	out[store.DocumentConstraints] = fallback(store.DocumentConstraints, s.Constraints())
	if g, revision, ok := s.Store.CurrentGoal(); ok {
		out[store.DocumentGoal] = documentView{Text: g.Statement, GoalID: g.ID, Revision: revision}
	}
	return out
}

func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"documents": s.documentsView()})
}

func (s *Server) documentHistory(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if !store.ValidDocumentKind(kind) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "未知文档：" + kind, "code": "document_unknown"})
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > 200 {
			writeErr(w, http.StatusBadRequest, "limit 必须是 1–200")
			return
		}
		limit = n
	}
	revisions := []documentRevision{}
	if kind == store.DocumentGoal {
		// The goal document's history is the goal row's own version journal; the
		// document kind has no rows of its own (the statement is the only copy).
		if g, ok := s.currentGoalDocument(); ok {
			for _, d := range s.Store.GoalDocumentHistory(g.ID) {
				revisions = append(revisions, documentRevision{Text: d.Text, Revision: d.Revision, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy})
			}
		}
		sort.SliceStable(revisions, func(i, j int) bool { return revisions[i].Revision > revisions[j].Revision })
		if len(revisions) > limit {
			revisions = revisions[:limit]
		}
		writeJSON(w, http.StatusOK, map[string]any{"revisions": revisions})
		return
	}
	for _, c := range s.Store.History("document", kind) {
		var d model.Document
		if err := json.Unmarshal(c.Value, &d); err != nil {
			continue
		}
		revisions = append(revisions, documentRevision{Text: d.Text, Revision: d.Revision, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy})
	}
	sort.SliceStable(revisions, func(i, j int) bool { return revisions[i].Revision > revisions[j].Revision })
	if len(revisions) > limit {
		revisions = revisions[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": revisions})
}

// documentAuthor names who wrote a project document revision: the operator, or
// the commander run that wrote it for them.
//
// The console credential is shared by the operator and the commander (D86), so
// "the request authenticated" never answered this question — the run identity
// attached by the auth gate is what distinguishes a model's write from a
// person's, and the run id is the durable name for it (the same id that appears
// in run history and in operator-actions.jsonl).
//
// An empty return is not a fallback: the store refuses an unsigned save, so a
// caller that cannot be attributed (a session identity without a run, or a bare
// X-Saker-Actor claim) is refused instead of being recorded as the operator.
func documentAuthor(r *http.Request) string {
	if id, ok := identityOf(r); ok {
		return strings.TrimSpace(id.RunID)
	}
	if actorOf(r) == collab.CommanderID {
		return ""
	}
	return collab.OperatorID
}

// saveDocument is the single write entry for all three documents. It compares
// the caller's revision before anything else, so a console that read an older
// version cannot overwrite a newer edit — including an edit made through the
// goal editor.
func (s *Server) saveDocument(w http.ResponseWriter, r *http.Request) {
	// The three project documents belong to the operator (agent.md L41). The route
	// is mounted only on the operator mux today, but the check is explicit here so
	// a future mount change cannot turn "any participant" into an operator edit:
	// the store writes updated_by=collab.OperatorID unconditionally, so without
	// this a model's write would be attested in the ledger as the operator's.
	if _, participant := identityOf(r); participant || actorOf(r) != collab.OperatorID {
		http.NotFound(w, r)
		return
	}
	kind := r.PathValue("kind")
	if !store.ValidDocumentKind(kind) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "未知文档：" + kind, "code": "document_unknown"})
		return
	}
	var req struct {
		Text     string `json:"text"`
		Revision uint64 `json:"revision"`
		GoalID   string `json:"goal_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if kind == store.DocumentGoal {
		_, ok := s.currentGoalDocument()
		if !ok {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "没有未收口的目标可写", "code": "document_no_open_goal"})
			return
		}
	}
	var doc model.Document
	var err error
	// The gate above admits only the operator, so this names the operator — but it
	// asks the same question the gate does instead of writing the constant, so a
	// later mount change cannot make the ledger lie about who saved.
	by := documentAuthor(r)
	if kind == store.DocumentGoal {
		doc, err = s.Store.SaveCurrentGoalStatement(req.Revision, req.GoalID, req.Text, by)
	} else {
		doc, err = s.Store.SaveDocument(req.Revision, kind, req.Text, by)
	}
	if errors.Is(err, store.ErrDocumentConflict) {
		current := s.documentsView()[kind]
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "文档已被改过：刷新看当前版再改",
			"code":    "document_conflict",
			"current": map[string]any{"revision": current.Revision, "updated_at": current.UpdatedAt, "goal_id": current.GoalID},
		})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if doc.Revision != req.Revision {
		s.recordAction("document_save", kind, doc.Text)
	}
	// A save whose text did not change writes nothing; the revision it reports is
	// the one that is really stored, and changed=false says why it did not move.
	writeJSON(w, http.StatusOK, map[string]any{
		"revision": doc.Revision, "updated_at": doc.UpdatedAt,
		"changed": doc.Revision != req.Revision,
	})
}
