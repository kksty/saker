package api

import (
	"net/http"
	"runtime"

	"github.com/kksty/saker/internal/platform"
)

// The platform boundary document answers one operator question read-only:
// "what isolation does this deployment actually enforce?". It exists because the
// taskbook requires process/network isolation to be documented, not claimed:
// Windows returns unsupported_boundary and Linux does not claim enforced
// isolation either. This endpoint only reports; no code may grant authority
// because a boundary is absent.
//
// It is status data, not ledger data: the schema is the response shape version
// (platform.BoundarySchema), never the store schema, so serving it needs no
// migration and no new object.

// platformBoundaryReport is the document for the platform this process runs on.
func platformBoundaryReport() platform.BoundaryReport {
	return platform.Report(runtime.GOOS, runtime.GOARCH)
}

// platformBoundary serves the read-only boundary status. It is registered on
// both the console mux and the participant mux: an executor that wants to know
// whether its tools are sandboxed must get the same answer as the operator.
func (s *Server) platformBoundary(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, platformBoundaryReport())
}

// registerPlatformBoundary wires the read-only route into a mux.
func (s *Server) registerPlatformBoundary(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/platform-boundary", s.platformBoundary)
}
