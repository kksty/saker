package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A polling status page must not spawn one process per request: the cached
// result serves GETs, and only the 检测 button (force) runs again.
func TestEgressProberCachesUntilForced(t *testing.T) {
	runs := 0
	p := &egressProber{doc: func() ([]byte, error) { return []byte("cyberhub: {}\n"), nil }}
	p.run = func(context.Context, []byte) (egressProbe, error) {
		runs++
		return egressProbe{OK: true, HTTPCode: 200, DurationMS: 42}, nil
	}

	first, err := p.Probe(context.Background(), false, "direct")
	if err != nil || !first.OK {
		t.Fatalf("first probe = %+v, %v", first, err)
	}
	if _, err := p.Probe(context.Background(), false, "direct"); err != nil {
		t.Fatalf("cached probe: %v", err)
	}
	if runs != 1 {
		t.Fatalf("cached read ran the probe %d times, want 1", runs)
	}
	if _, err := p.Probe(context.Background(), true, "direct"); err != nil {
		t.Fatalf("forced probe: %v", err)
	}
	if runs != 2 {
		t.Fatalf("forced read ran the probe %d times, want 2", runs)
	}

	// A subscription probe refetches the feed, so its result lives longer.
	if p.ttl("subscription") <= p.ttl("single") {
		t.Fatalf("subscription TTL %s must exceed single-URL TTL %s", p.ttl("subscription"), p.ttl("single"))
	}
}

// An un-runnable probe is not a verdict about the network: the caller gets an
// error the page renders as "probe_error", not a red light.
func TestEgressProberReportsUnavailableDocument(t *testing.T) {
	p := &egressProber{}
	if _, err := p.Probe(context.Background(), false, "direct"); err == nil {
		t.Fatal("probe without a document must fail")
	}
	broken := &egressProber{doc: func() ([]byte, error) { return nil, errors.New("no document") }}
	if _, err := broken.Probe(context.Background(), false, "direct"); err == nil {
		t.Fatal("document error must surface")
	}
}

// The console's status strip is operator-only: an unlisted /api segment is
// hidden from harness sessions, and this route must stay in that default.
func TestEgressRouteStaysOperatorOnly(t *testing.T) {
	if !agentRouteHidden("/api/egress") {
		t.Fatal("/api/egress is visible to harness sessions")
	}
}

func TestEgressProberWithoutDocumentOrBinaryFails(t *testing.T) {
	p := newEgressProber("", nil)
	if _, err := p.Probe(context.Background(), true, "direct"); err == nil {
		t.Fatal("prober without a document must fail")
	}
	// runProcess without a binary is an error, not a fabricated result.
	probe, err := p.runProcess(context.Background(), []byte("cyberhub: {}\n"))
	if err == nil {
		t.Fatalf("expected a missing-binary error, got %+v", probe)
	}
}

// The rail chip and the 30-second poll read the cache only: browsing a project
// must not turn into continuous outbound probes.
func TestEgressProberCachedDoesNotRun(t *testing.T) {
	runs := 0
	p := &egressProber{doc: func() ([]byte, error) { return []byte("cyberhub: {}\n"), nil }}
	p.run = func(context.Context, []byte) (egressProbe, error) {
		runs++
		return egressProbe{OK: true, HTTPCode: 200}, nil
	}
	if p.Cached() != nil {
		t.Fatal("cache must start empty")
	}
	if runs != 0 {
		t.Fatal("Cached() ran the probe")
	}
	if _, err := p.Probe(context.Background(), true, "direct"); err != nil {
		t.Fatalf("forced probe: %v", err)
	}
	if p.Cached() == nil {
		t.Fatal("cache missing after a forced probe")
	}
	if runs != 1 {
		t.Fatalf("runs = %d, want 1", runs)
	}
}

// Regression: Probe used to hold the cache lock across the subprocess, so every
// cached read (the rail chip polls it on every page) waited up to the process
// budget for a probe it did not start. Reading the cache must stay immediate
// while a forced probe is in flight.
func TestEgressCachedDoesNotWaitForAnInFlightProbe(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	p := &egressProber{doc: func() ([]byte, error) { return []byte("cyberhub: {}\n"), nil }}
	p.run = func(context.Context, []byte) (egressProbe, error) {
		close(started)
		<-release
		return egressProbe{OK: true, HTTPCode: 200}, nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := p.Probe(context.Background(), true, "direct"); err != nil {
			t.Errorf("forced probe: %v", err)
		}
	}()
	<-started

	got := make(chan *egressProbe, 1)
	go func() { got <- p.Cached() }()
	select {
	case cached := <-got:
		if cached != nil {
			t.Fatalf("cache was populated before the probe finished: %+v", cached)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cached blocked behind an in-flight probe")
	}
	close(release)
	<-done
	if probe := p.Cached(); probe == nil || !probe.OK {
		t.Fatalf("cache missing after the probe finished: %+v", probe)
	}
}
