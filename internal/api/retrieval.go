package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kksty/saker/internal/collab"
	"github.com/kksty/saker/internal/retrieval"
)

func factQuery(r *http.Request) retrieval.FactQuery {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	return retrieval.FactQuery{Session: harnessSession(r), Mode: q.Get("mode"), Q: q.Get("q"), Kind: q.Get("kind"), Intent: q.Get("intent"), Status: q.Get("status"), Tag: q.Get("tag"), Cursor: q.Get("cursor"), Limit: limit, Ascending: q.Get("order") == "oldest", Sort: q.Get("sort")}
}
func retrievalError(w http.ResponseWriter, e error) {
	var incomplete *retrieval.IndexIncomplete
	if errors.As(e, &incomplete) {
		writeJSON(w, 409, map[string]any{"error": e.Error(), "code": "semantic_index_incomplete", "indexed": incomplete.Indexed, "total": incomplete.Total, "index_url": "/api/retrieval/index"})
		return
	}
	code := http.StatusBadRequest
	if strings.Contains(e.Error(), "expired") || strings.Contains(e.Error(), "conflict") {
		code = http.StatusConflict
	}
	if strings.Contains(e.Error(), "unconfigured") {
		code = http.StatusNotImplemented
	}
	writeErr(w, code, e.Error())
}
func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	out, e := retrieval.Changes(s.Store, r.URL.Query().Get("cursor"), limit, since)
	if e != nil {
		retrievalError(w, e)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) graphQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	edgeLimit, _ := strconv.Atoi(q.Get("edge_limit"))
	boundaryLimit, _ := strconv.Atoi(q.Get("boundary_limit"))
	maxBytes, _ := strconv.Atoi(q.Get("max_bytes"))
	depth := 2
	if q.Has("depth") {
		depth, _ = strconv.Atoi(q.Get("depth"))
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	roots := []string{}
	if q.Get("root") != "" {
		roots = strings.Split(q.Get("root"), ",")
	}
	out, e := retrieval.Graph(s.Store, retrieval.GraphQuery{Session: harnessSession(r), Roots: roots, Direction: q.Get("direction"), Depth: depth, Order: q.Get("order"), Projection: q.Get("projection"), Cursor: q.Get("cursor"), Limit: limit, EdgeLimit: edgeLimit, BoundaryLimit: boundaryLimit, MaxBytes: maxBytes})
	if e != nil {
		retrievalError(w, e)
		return
	}
	if _, agent := identityOf(r); agent {
		out.Goals = nil
		for i := range out.Intents {
			in := &out.Intents[i]
			in.Doc = ""
			in.Notes = nil
			in.Interjections = nil
			in.Handoff = nil
			in.Eval = ""
		}
	}
	writeJSON(w, 200, out)
}

// contextReader names the working-context delivery a request may read or ACK.
// Only a harness session has one: an executor reads its own direction's page
// through its per-run identity, and the commander reads its page through the
// project mux, where its identity is deliberately not mounted on unscoped reads
// but the gate marks the session surface.
//
// A console/operator request is neither. Falling back to "commander" for it was
// not a harmless default: getting the context materialises the next delivery
// (WorkingContext saves one when none is pending), the heartbeat reads a due
// delivery as unacknowledged work and buys a full round, and that round could
// dispatch real work. Merely inspecting the context from the console must not
// wake the commander, so an unmarked caller gets no delivery at all (404).
func contextReader(r *http.Request) (string, bool) {
	if id, ok := identityOf(r); ok {
		switch {
		case id.IntentID != "":
			return "intent:" + id.IntentID, true
		case id.ParticipantID == collab.CommanderID:
			// Some commander writes carry the identity; the context route is not
			// tagged, but the same caller is still the commander's own page.
			return "commander", true
		default:
			// A purpose run without a direction owns no direction delivery.
			return "", false
		}
	}
	if IsAgentSurface(r.Context()) {
		return "commander", true
	}
	return "", false
}
func (s *Server) workingContext(w http.ResponseWriter, r *http.Request) {
	reader, ok := contextReader(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	out, e := retrieval.WorkingContext(s.Store, reader)
	if e != nil {
		retrievalError(w, e)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) acknowledgeContext(w http.ResponseWriter, r *http.Request) {
	reader, ok := contextReader(r)
	if !ok {
		// ACK is the same session-only surface. An operator ACK would clear the
		// commander's pending delivery and advance its attention checkpoint,
		// silently swallowing the events that page carried.
		http.NotFound(w, r)
		return
	}
	var req struct {
		ID string `json:"delivery_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid json: "+err.Error())
		return
	}
	if e := s.Store.AcknowledgeDelivery(reader, req.ID); e != nil {
		retrievalError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"acknowledged": req.ID})
}
func (s *Server) factHistory(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		http.NotFound(w, r)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := retrieval.History(s.Store, "fact", r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		retrievalError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) graphExport(w http.ResponseWriter, r *http.Request) {
	if harnessSession(r) {
		http.NotFound(w, r)
		return
	}
	db, v := s.Store.ReadView()
	w.Header().Set("Content-Disposition", "attachment; filename=graph.json")
	writeJSON(w, 200, map[string]any{"snapshot_version": v, "facts": db.Facts, "intents": db.Intents, "goals": db.Goals, "evidence": db.Evidence, "reviews": db.Reviews})
}

func (s *Server) experienceCandidates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := retrieval.Experiences(s.Store, retrieval.ExperienceQuery{Q: q.Get("q"), Intent: q.Get("intent"), Cursor: q.Get("cursor"), Limit: limit})
	if err != nil {
		retrievalError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
