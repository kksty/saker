package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A project-scoped path that reached the instance base is a routing mistake the
// caller can fix, so the 404 names the base. Any other unknown path is just
// missing: advising /p/<id> there sends the caller looking in the wrong place,
// and the old text also claimed only two instance routes existed.
func TestUnknownPathsRouteHintOnlyForAPIPaths(t *testing.T) {
	h := &hub{kits: map[string]*kit{}}

	api := httptest.NewRecorder()
	h.ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/api/intents", nil))
	if api.Code != http.StatusNotFound {
		t.Fatalf("/api/intents = %d, want 404", api.Code)
	}
	if hint := errorText(t, api.Body.Bytes()); !strings.Contains(hint, "/p/<项目>/api/") {
		t.Fatalf("project-base hint missing: %s", hint)
	}

	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/no-such-page", nil))
	if page.Code != http.StatusNotFound {
		t.Fatalf("/no-such-page = %d, want 404", page.Code)
	}
	if hint := errorText(t, page.Body.Bytes()); strings.Contains(hint, "/p/") {
		t.Fatalf("non-API miss got routing advice: %s", hint)
	}
}

func errorText(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("404 body is not JSON: %v (%s)", err, body)
	}
	return payload.Error
}
