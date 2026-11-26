package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kksty/saker/internal/model"
)

// callRaw routes one request with a body that may be deliberately malformed
// through the server's real mux: call() always encodes a value, so it cannot
// express a truncated request.
func callRaw(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.Register(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// A truncated request must be a 400, not the widest action the request can name:
// an empty goal_id resumes the whole project, so defaulting the zero value
// performs more than the caller asked for.
func TestResumeRejectsMalformedBody(t *testing.T) {
	s, _ := newBindingServer(t)
	if rec := call(t, s, http.MethodPost, "/api/project/pause", nil); rec.Code != http.StatusOK {
		t.Fatalf("pause = %d: %s", rec.Code, rec.Body.String())
	}
	rec := callRaw(t, s, http.MethodPost, "/api/project/resume", `{"goal_id":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed resume = %d: %s, want 400", rec.Code, rec.Body.String())
	}
	if !s.paused.Load() {
		t.Fatal("a truncated body resumed the project: the zero value became the widest action")
	}
	if !strings.Contains(rec.Body.String(), "不是合法 JSON") {
		t.Fatalf("refusal does not say what is wrong: %s", rec.Body.String())
	}
}

// Decoder.Decode stops at the first complete value, so a body that continues
// after it would otherwise be read as a valid request. Trailing garbage and a
// second concatenated object are both malformed requests, not something to
// ignore after the part we happened to parse.
func TestOptionalBodyRejectsTrailingGarbage(t *testing.T) {
	s, _ := newBindingServer(t)
	// A real goal id matters: a made-up one would make the request fail with
	// "没有这个目标" and the 400 would not prove anything about the trailing bytes.
	goal, err := s.Store.AddGoal(model.Goal{Statement: "尾部垃圾"})
	if err != nil {
		t.Fatal(err)
	}
	if rec := call(t, s, http.MethodPost, "/api/project/pause", nil); rec.Code != http.StatusOK {
		t.Fatalf("pause = %d: %s", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "text after the value", body: fmt.Sprintf(`{"goal_id":%q}zzz`, goal.ID)},
		{name: "second object", body: fmt.Sprintf(`{"goal_id":%q}{"goal_id":%q}`, goal.ID, goal.ID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := callRaw(t, s, http.MethodPost, "/api/project/resume", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("resume with %q = %d: %s, want 400", tc.body, rec.Code, rec.Body.String())
			}
			if !s.paused.Load() {
				t.Fatal("a body with trailing content resumed the project")
			}
		})
	}
	// continue answers with its own machine code; it shares the same rule.
	rec := callRaw(t, s, http.MethodPost, "/api/intents/in_x/continue", `{"note":"x"}zzz`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("continue with trailing garbage = %d: %s, want 400", rec.Code, rec.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "continue_invalid" {
		t.Fatalf("continue code = %q, want continue_invalid", body.Code)
	}
}

// A bodyless request is legal on every endpoint that accepts an optional body:
// the decoder reads an EOF as "no fields". This pins that half of the rule —
// without it, turning an empty body into a 400 would silently break resume, park
// and the commander's standby route, and no test would go red.
func TestOptionalBodyAcceptsEmptyBody(t *testing.T) {
	s, _ := newBindingServer(t)
	// A resumable project needs something to continue: an open goal.
	if _, err := s.Store.AddGoal(model.Goal{Statement: "空 body 仍受理"}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", "  \n"} {
		if rec := call(t, s, http.MethodPost, "/api/project/pause", nil); rec.Code != http.StatusOK {
			t.Fatalf("pause = %d: %s", rec.Code, rec.Body.String())
		}
		rec := callRaw(t, s, http.MethodPost, "/api/project/resume", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("resume with body %q = %d: %s, want 200", body, rec.Code, rec.Body.String())
		}
		if s.paused.Load() {
			t.Fatalf("resume with body %q did not resume the project", body)
		}
	}
	// The empty body must reach the handler, not the decoder: park's own rule
	// refuses it with a reason ("没有理由的停放等于丢工作"), which a decode-level
	// 400 would replace — so the domain answer is the proof.
	in, err := s.Store.CreateIntent(model.Intent{Title: "empty body park"})
	if err != nil {
		t.Fatal(err)
	}
	rec := callRaw(t, s, http.MethodPost, "/api/intents/"+in.ID+"/park", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("empty-body park = %d: %s, want the domain 409, not a decode 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "停放必须写理由") {
		t.Fatalf("empty-body park did not reach the handler's own rule: %s", rec.Body.String())
	}
}

// The other endpoints that accept an optional body answer the same way: a broken
// body is a bad request, never a defaulted request. continue keeps its machine
// code so the console can show it as a request error.
func TestOptionalBodyEndpointsRejectMalformedJSON(t *testing.T) {
	s, _ := newBindingServer(t)
	for _, tc := range []struct {
		name string
		path string
		code string
	}{
		{name: "continue", path: "/api/intents/in_x/continue", code: "continue_invalid"},
		{name: "pin goal", path: "/api/goals/g_x/met"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := callRaw(t, s, http.MethodPost, tc.path, `{"broken":`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d: %s, want 400", tc.path, rec.Code, rec.Body.String())
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s: %v (%s)", tc.path, err, rec.Body.String())
			}
			if tc.code != "" && body.Code != tc.code {
				t.Fatalf("%s code = %q, want %q", tc.path, body.Code, tc.code)
			}
		})
	}
}
