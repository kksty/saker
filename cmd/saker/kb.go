package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/kksty/saker/internal/kb"
)

// kbEntryResponse is one served entry plus the layer that supplied it. The
// console needs built-in and instance knowledge told apart; the stored entry
// JSON itself stays free of instance metadata.
type kbEntryResponse struct {
	kb.Entry
	Origin        string `json:"origin"`
	CoversBuiltin bool   `json:"covers_builtin,omitempty"`
}

func kbEntriesResponse(entries []kb.Entry) []kbEntryResponse {
	out := make([]kbEntryResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, kbEntryResponse{Entry: e, Origin: e.Origin(), CoversBuiltin: e.CoversBuiltin()})
	}
	return out
}

func (h *hub) serveKBManagement(w http.ResponseWriter, r *http.Request) {
	lib := h.cfg.KB
	if lib == nil {
		writeJSON(w, 503, map[string]any{"error": "知识库未配置"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/kb/")
	if strings.HasPrefix(path, "entries/") && r.Method == http.MethodDelete {
		var in struct {
			Revision string `json:"revision"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
			kbError(w, err)
			return
		}
		if err := lib.Delete(strings.TrimPrefix(path, "entries/"), in.Revision); err != nil {
			kbError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"deleted": true})
		return
	}
	if path == "reload" && r.Method == http.MethodPost {
		if err := lib.Reload(); err != nil {
			kbError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"entries": kbEntriesResponse(lib.Entries())})
		return
	}
	if path == "candidates" {
		switch r.Method {
		case http.MethodGet:
			rows, err := lib.Candidates()
			if err != nil {
				kbError(w, err)
				return
			}
			writeJSON(w, 200, map[string]any{"candidates": rows})
		case http.MethodPost:
			var c kb.Candidate
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&c); err != nil {
				kbError(w, err)
				return
			}
			c.ID, c.Version, c.Origin = "", 0, "operator"
			row, err := lib.SaveCandidate(c)
			if err != nil {
				kbError(w, err)
				return
			}
			writeJSON(w, 201, row)
		default:
			writeJSON(w, 405, map[string]any{"error": "只支持 GET / POST"})
		}
		return
	}
	id := strings.TrimPrefix(path, "candidates/")
	if strings.HasPrefix(path, "candidates/") && !strings.Contains(id, "/") && r.Method == http.MethodDelete {
		var in struct {
			Version int `json:"version"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
			kbError(w, err)
			return
		}
		if err := lib.DeleteCandidate(id, in.Version); err != nil {
			kbError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"deleted": true})
		return
	}
	if strings.HasPrefix(path, "candidates/") && strings.HasSuffix(id, "/publish") && r.Method == http.MethodPost {
		id = strings.TrimSuffix(id, "/publish")
		var in struct {
			Version int `json:"version"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
			kbError(w, err)
			return
		}
		entry, err := lib.Publish(id, in.Version)
		if err != nil {
			kbError(w, err)
			return
		}
		writeJSON(w, 200, entry)
		return
	}
	if strings.HasPrefix(path, "candidates/") && !strings.Contains(id, "/") && r.Method == http.MethodPut {
		var c kb.Candidate
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&c); err != nil {
			kbError(w, err)
			return
		}
		c.ID = id
		row, err := lib.SaveCandidate(c)
		if err != nil {
			kbError(w, err)
			return
		}
		writeJSON(w, 200, row)
		return
	}
	writeJSON(w, 405, map[string]any{"error": "不支持的知识管理操作"})
}

func kbError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, kb.ErrConflict) {
		status = http.StatusConflict
	}
	if errors.Is(err, os.ErrNotExist) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
