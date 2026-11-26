package main

import (
	"net/http"
	"testing"
)

// The review credential's read surface is an allowlist, so a route added later
// stays absent until someone names it here on purpose. This table is that
// contract, including the denials that carry the reason from the doc comment:
// another reviewer's verdict, the view of the party under review, and every
// write.
func TestVerifierReadAllowed(t *testing.T) {
	p := "/p/p1/api"
	cases := []struct {
		method, path string
		want         bool
	}{
		// The surface: the fact, its history, its evidence, what a run did.
		{http.MethodGet, p + "/facts", true},
		{http.MethodGet, p + "/facts/f_1", true},
		{http.MethodGet, p + "/facts/f_1/history", true},
		{http.MethodGet, p + "/evidence", true},
		{http.MethodGet, p + "/evidence/e_1", true},
		{http.MethodGet, p + "/runs/r_1/events", true},

		// Absent on purpose.
		{http.MethodGet, p + "/reviews", false},
		{http.MethodGet, p + "/board", false},
		{http.MethodGet, p + "/intents", false},
		{http.MethodGet, p + "/goals", false},
		{http.MethodGet, p + "/messages", false},
		{http.MethodGet, p + "/runs", false},
		{http.MethodGet, p + "/runs/r_1/output", false},
		{http.MethodGet, p + "/runs/r_1/context", false},
		{http.MethodGet, p + "/runs/r_1/events/tail", false},
		{http.MethodGet, p + "/facts/verify-pending", false},
		{http.MethodPost, p + "/facts/f_1/verify", false},
		{http.MethodPut, p + "/state", false},

		// Only the project base is on the surface; instance and static paths are
		// the operator's.
		{http.MethodGet, "/api/facts", false},
		{http.MethodGet, "/p/p1/static/index.js", false},
	}
	for _, c := range cases {
		if got := verifierReadAllowed(c.method, c.path); got != c.want {
			t.Errorf("verifierReadAllowed(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
