package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/kksty/saker/internal/kb"
)

// kbBrief is a search hit: enough to judge whether an entry is worth opening,
// not its body. The caller's context window is the scarce resource here; a hit
// list must not carry every matched entry's detail. The body is opened one
// entry at a time with ?id=.
type kbBrief struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// HasDetail and NCommands say what opening the entry would cost and add
	// without reading it: a payload-only entry and a long write-up read very
	// differently, and the caller decides per hit.
	HasDetail bool `json:"has_detail"`
	NCommands int  `json:"n_commands"`
}

func brief(e kb.Entry) kbBrief {
	return kbBrief{
		ID:          e.ID,
		Title:       e.Title,
		Description: e.Description,
		Tags:        e.Tags,
		HasDetail:   strings.TrimSpace(e.Detail) != "",
		NCommands:   len(e.Commands),
	}
}

// worldKB reads the offline technique library: a brief hit list, or one whole
// entry with ?id=. Search stays cheap; the body is paid only for the entry the
// caller opens.
func (s *Server) worldKB(w http.ResponseWriter, r *http.Request) {
	if s.KB == nil {
		writeErr(w, 503, "知识库未配置")
		return
	}
	q := r.URL.Query()
	if id := strings.TrimSpace(q.Get("id")); id != "" {
		for _, e := range s.KB.Entries() {
			if e.ID == id {
				writeJSON(w, 200, map[string]any{"entry": e, "revision": kb.Revision(e)})
				return
			}
		}
		writeErr(w, 404, "知识条目不存在")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	entries := s.KB.Search(q.Get("q"), strings.Split(q.Get("tags"), ","), limit)
	// Revisions stay whole: the hit list is also the edit-token prefix, and a
	// reader that edits an entry needs the same token whether or not it opened
	// the body first.
	briefs := make([]kbBrief, 0, len(entries))
	for _, e := range entries {
		briefs = append(briefs, brief(e))
	}
	writeJSON(w, 200, map[string]any{"entries": briefs, "revisions": kb.Revisions(entries)})
}

func (s *Server) nominateWorldKB(w http.ResponseWriter, r *http.Request) {
	id, ok := identityOf(r)
	if !ok {
		writeErr(w, 401, "缺少协作身份")
		return
	}
	s.saveWorldNomination(w, r, fmt.Sprintf("project:%s participant:%s run:%s", id.ProjectID, id.ParticipantID, id.RunID))
}

func (s *Server) saveWorldNomination(w http.ResponseWriter, r *http.Request, origin string) {
	if s.KB == nil {
		writeErr(w, 503, "知识库未配置")
		return
	}
	var c kb.Candidate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&c); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	c.ID, c.Version = "", 0
	c.Origin = origin
	row, err := s.KB.SaveCandidate(c)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, kb.ErrConflict) {
			status = http.StatusConflict
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, 201, row)
}

func (s *Server) nominateProjectWorldKB(w http.ResponseWriter, r *http.Request) {
	projectID := ""
	if s.Project != nil {
		projectID = s.Project.ID
	}
	s.saveWorldNomination(w, r, fmt.Sprintf("project:%s actor:%s", projectID, actorOf(r)))
}
