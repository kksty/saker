package main

import (
	"net"
	"net/http"
	"testing"
	"time"
)

// The cleanup budget must outlive the drain window plus the host stop. At 10s the
// process exited while DrainRunning was still waiting for executors to write down
// evidence; with the host stop sharing that same window, a full drain left the Hub
// and scan listening after saker was gone.
func TestShutdownCleanupGraceExceedsDrainWindow(t *testing.T) {
	if shutdownCleanupGrace <= shutdownDrainTimeout {
		t.Fatalf("shutdownCleanupGrace=%s must exceed shutdownDrainTimeout=%s", shutdownCleanupGrace, shutdownDrainTimeout)
	}
	if shutdownCleanupGrace < shutdownDrainTimeout+hostStopBudget {
		t.Fatalf("shutdownCleanupGrace=%s leaves no room for a %s host stop after a %s drain",
			shutdownCleanupGrace, hostStopBudget, shutdownDrainTimeout)
	}
	if hostStopBudget <= 0 {
		t.Fatalf("hostStopBudget=%s must be positive: a zero budget skips the host stop", hostStopBudget)
	}
}

func TestShutdownClosesListenerEvenWhenCleanupBlocks(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.NotFoundHandler()}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	blocked, done := make(chan struct{}), make(chan struct{})
	defer close(blocked)
	go func() { shutdownConsole(srv, 30*time.Millisecond, func() { <-blocked }); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup trapped shutdown")
	}
	select {
	case err := <-served:
		if err != http.ErrServerClosed {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener stayed open")
	}
}
