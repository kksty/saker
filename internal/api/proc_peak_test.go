package api

import (
	"testing"

	"github.com/kksty/saker/internal/host"
	"github.com/kksty/saker/internal/model"
)

// The record has to answer "which process, in which round" from the journal
// alone: a settled run must carry one proc_peak event with pid, name, peak bytes
// and whether the reading was available.
func TestSettledRunRecordsChildPeaks(t *testing.T) {
	s, _ := newBindingServer(t)
	run, err := s.Store.AddRun(model.Run{Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, s, run.ID)
	defer s.closeJournal(run.ID)
	s.ChildPeaks = func() []host.PeakRSS {
		return []host.PeakRSS{
			{PID: 4242, Name: "cyber-scan", Bytes: 512 << 20, Available: true},
			{PID: 4243, Reason: "进程已退出"},
		}
	}
	s.recordChildPeaks(run.ID)

	var event map[string]any
	for _, line := range readJournalFileLines(t, s.DataDir, run.ID) {
		if line["kind"] == "proc_peak" {
			event = line
		}
	}
	if event == nil {
		t.Fatal("no proc_peak event in the journal")
	}
	processes, _ := event["processes"].([]any)
	if len(processes) != 2 {
		t.Fatalf("processes = %v, want the two samples", event["processes"])
	}
	first, _ := processes[0].(map[string]any)
	if first["pid"] != float64(4242) || first["name"] != "cyber-scan" {
		t.Fatalf("first sample = %v", first)
	}
	if first["peak_rss_bytes"] != float64(512<<20) || first["available"] != true {
		t.Fatalf("first sample bytes/available = %v", first)
	}
	second, _ := processes[1].(map[string]any)
	if second["available"] != false || second["reason"] == "" {
		t.Fatalf("an unreadable sample must stay unavailable with a reason: %v", second)
	}
}

// Crossing the threshold adds exactly one warning line; it must not change the
// run's status or stop anything. Below the threshold there is no warning at all.
func TestPeakOverThresholdAddsOneWarning(t *testing.T) {
	s, _ := newBindingServer(t)
	run, err := s.Store.AddRun(model.Run{Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, s, run.ID)
	defer s.closeJournal(run.ID)
	s.ChildPeaks = func() []host.PeakRSS {
		return []host.PeakRSS{{PID: 9001, Name: "python3", Bytes: host.PeakRSSWarnBytes + (1 << 30), Available: true}}
	}
	s.recordChildPeaks(run.ID)
	s.recordChildPeaks(run.ID) // a second settle path must not duplicate the line
	s.flushRunWarnings(run.ID)

	got, _ := s.Store.GetRun(run.ID)
	if len(got.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", got.Warnings)
	}
	if got.Status != model.RunRunning {
		t.Fatalf("status = %q: a memory sample must not settle the run", got.Status)
	}
	warnings := 0
	for _, line := range readJournalFileLines(t, s.DataDir, run.ID) {
		if line["kind"] == "warning" {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("warning events = %d, want 1", warnings)
	}
}

// Under the threshold the sample is recorded and nothing warns.
func TestPeakUnderThresholdRecordsWithoutWarning(t *testing.T) {
	s, _ := newBindingServer(t)
	run, err := s.Store.AddRun(model.Run{Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, s, run.ID)
	defer s.closeJournal(run.ID)
	s.ChildPeaks = func() []host.PeakRSS {
		return []host.PeakRSS{
			{PID: 1, Name: "cyber-harness", Bytes: host.PeakRSSWarnBytes, Available: true}, // exactly the threshold
			{PID: 2, Name: "python3", Available: false, Reason: "已退出"},
		}
	}
	s.recordChildPeaks(run.ID)
	s.flushRunWarnings(run.ID)

	got, _ := s.Store.GetRun(run.ID)
	if len(got.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none: the threshold is exceeded, not reached", got.Warnings)
	}
	events := 0
	for _, line := range readJournalFileLines(t, s.DataDir, run.ID) {
		if line["kind"] == "proc_peak" {
			events++
		}
	}
	if events != 1 {
		t.Fatalf("proc_peak events = %d, want 1 even when nothing is large", events)
	}
}

// A server without the seam (bare tests, audit-only instance) records nothing:
// there is no host to sample, and a fabricated "no processes" line would be a
// false statement about the machine.
func TestNoPeakSeamRecordsNothing(t *testing.T) {
	s, _ := newBindingServer(t)
	run, err := s.Store.AddRun(model.Run{Node: "local"})
	if err != nil {
		t.Fatal(err)
	}
	openTestJournal(t, s, run.ID)
	defer s.closeJournal(run.ID)
	s.recordChildPeaks(run.ID)
	for _, line := range readJournalFileLines(t, s.DataDir, run.ID) {
		if line["kind"] == "proc_peak" {
			t.Fatalf("recorded a sample without a seam: %v", line)
		}
	}
}
