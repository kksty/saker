package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kksty/saker/internal/retrieval"
)

// focusPage is the read side of the focus projection. It is a query view over
// the one ledger, not a second store: the participant surface sees only its own
// direction, the operator/commander surface may name any direction, and neither
// can write through it.
func (s *Server) focusPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	intent := strings.TrimSpace(q.Get("intent"))
	if id, ok := identityOf(r); ok {
		// A participant's identity is the run's frozen direction. Honouring an
		// intent in the query would let one direction read another's hot facts,
		// so a mismatch is a refusal, not a silent fallback. A commander calls
		// the project surface (no participant identity) and must name its
		// intent explicitly.
		if id.IntentID == "" {
			writeErr(w, 403, "本轮协作身份没有意图")
			return
		}
		if intent != "" && intent != id.IntentID {
			writeErr(w, 403, "只能读取自己意图的焦点")
			return
		}
		intent = id.IntentID
	}
	if intent == "" {
		writeErr(w, 400, "intent 参数必填：焦点按任务计算")
		return
	}
	if _, ok := s.Store.GetIntent(intent); !ok {
		writeErr(w, 404, "意图不存在")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := retrieval.Focus(s.Store, retrieval.FocusQuery{
		Intent: intent,
		Layer:  q.Get("layer"),
		Cursor: q.Get("cursor"),
		Limit:  limit,
	})
	if err != nil {
		retrievalError(w, err)
		return
	}
	if harnessSession(r) {
		for i := range out.Facts {
			item := &out.Facts[i]
			fact, _, ok := s.Store.FactVersion(item.ID)
			if !ok || fact.Status != "verified" || fact.ClaimVersion != item.ClaimVersion {
				item.Title, item.Summary = "", ""
			}
		}
		redactFocusProse(out.Facts)
	}
	writeJSON(w, 200, out)
}

// Preserve focus ordering and routing metadata while withholding prose for
// assertions that have not been verified.
func redactFocusProse(facts []retrieval.FocusFact) {
	for i := range facts {
		if facts[i].Status != "verified" {
			facts[i].Title, facts[i].Summary = "", ""
		}
	}
}

// registerFocusRoutes mounts the focus read endpoint on one HTTP surface. The
// caller adds it wherever the retrieval contract is already exposed: the
// operator/project mux (which the commander uses to call the control plane) and
// the participant mux (bound to the run identity). Registering the same route
// on both is intentional; the handlers differ only by the identity in the
// request context.
func (s *Server) registerFocusRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/focus", s.focusPage)
}
