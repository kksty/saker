package main

import "net/http"

// serveModelCheck runs the settings page's explicit connectivity probe: one
// lightweight inference through the live host when it is online, the direct
// probe otherwise. It never issues a request unless the operator asks for one.
func (h *hub) serveModelCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, 405, map[string]any{"error": "只支持 POST"})
		return
	}
	writeJSON(w, 200, h.checkModel(r.Context()))
}
