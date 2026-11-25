package api

import "net/http"

// consoleWorkers is GET /p/<project>/api/workers.
func (s *Server) consoleWorkers(w http.ResponseWriter, r *http.Request) {
	s.workersRoster(w, workerCaller{participant: actorOf(r), operator: true})
}

// consoleKillWorker is POST /p/<project>/api/workers/{id}/kill.
func (s *Server) consoleKillWorker(w http.ResponseWriter, r *http.Request) {
	s.killWorker(w, r, workerCaller{participant: actorOf(r), operator: true})
}
