package main

import (
	"net/http"
	"strings"
)

// maxRequestBody is the instance's backstop for request bodies. Handlers that
// accept a small JSON document already cap themselves tighter (1 KiB–2 MiB);
// this is for the ones that decode straight from r.Body (a new handler, or an
// old one that forgot). 16 MiB is far above the largest legitimate payload —
// a 2 MiB login-material document plus its envelope — so no feature changes
// behavior, while a multi-gigabyte body is refused at the first read instead of
// being decoded into memory.
const maxRequestBody = 16 << 20

// maxProjectImportBody is the one deliberate exception: a whole-project archive
// carries the ledger, evidence and case files, so it is legitimately hundreds of
// MB. The route is operator-only and the body goes straight to a temp file, not
// into memory. Four GiB leaves room for a large engagement without turning the
// console into an unbounded upload endpoint.
const maxProjectImportBody = 4 << 30

// withBodyLimit wraps every request body in http.MaxBytesReader before routing.
// It also rejects a declared Content-Length over the limit immediately, so the
// common case fails without reading anything; a chunked or lying client still
// hits the reader cap. A handler that ignores its decode error cannot eat an
// unbounded body: the read itself fails at the limit.
func withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(maxRequestBody)
		// The archive upload is raw bytes, not JSON. A JSON import (server-side
		// path) stays at the ordinary cap: it is a small document.
		if r.Method == http.MethodPost && r.URL.Path == "/api/projects/import" &&
			!strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))), "application/json") {
			limit = maxProjectImportBody
		}
		if r.ContentLength > limit {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "请求体过大"})
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}
