package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The edge cap is the backstop for handlers that decode straight from r.Body:
// an oversized body must fail before the handler can turn it into memory, and a
// normal body must pass through untouched.
func TestBodyLimitAllowsOnlyArchiveUploadThroughTheLargeCap(t *testing.T) {
	seen := ""
	h := withBodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		seen = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	// A declared size over the ordinary cap is accepted on the one route whose
	// body is a project archive, and only for a non-JSON content type.
	req := httptest.NewRequest(http.MethodPost, "/api/projects/import", strings.NewReader("archive"))
	req.Header.Set("Content-Type", "application/zip")
	req.ContentLength = maxRequestBody + 1
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK || seen != "archive" {
		t.Fatalf("archive upload = %d, seen %q", res.Code, seen)
	}

	// The JSON form of the same route is still a small path document.
	req = httptest.NewRequest(http.MethodPost, "/api/projects/import", strings.NewReader(`{"path":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = maxRequestBody + 1
	res = httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized JSON import = %d, want 413", res.Code)
	}
}

func TestBodyLimitRejectsOversizedRequests(t *testing.T) {
	seen := ""
	h := withBodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		seen = string(body)
		w.WriteHeader(http.StatusOK)
	}))

	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader("ok")))
	if res.Code != http.StatusOK || seen != "ok" {
		t.Fatalf("small body = %d, seen %q", res.Code, seen)
	}

	// Content-Length is present, so the middleware can answer without a read.
	res = httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(strings.Repeat("x", maxRequestBody+1))))
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized declared body = %d, want 413", res.Code)
	}

	// No Content-Length (as with a chunked upload): the reader cap still stops
	// the handler once it crosses the limit.
	res = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(strings.Repeat("x", maxRequestBody+1)))
	req.ContentLength = -1
	h.ServeHTTP(res, req)
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized chunked body = %d, want 413", res.Code)
	}
}
