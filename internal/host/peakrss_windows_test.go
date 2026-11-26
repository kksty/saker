//go:build windows

package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A live child has a real peak working set; the reader must return it, not a
// placeholder. The helper is this test binary itself, so the expected process
// name is known and the child cannot exit on its own within the window.
func TestWindowsPeakRSSReadsARealChild(t *testing.T) {
	if os.Getenv("SAKER_PEAK_HELPER") == "1" {
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestWindowsPeakRSSReadsARealChild")
	cmd.Env = append(os.Environ(), "SAKER_PEAK_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	peak := windowsPeakRSS(cmd.Process.Pid)
	if !peak.Available {
		t.Fatalf("peak = %+v, want an available reading", peak)
	}
	if peak.Bytes == 0 {
		t.Fatalf("peak = %+v: a live test binary cannot have a 0-byte peak", peak)
	}
	if want := filepath.Base(os.Args[0]); peak.Name != want {
		t.Fatalf("name = %q, want %q", peak.Name, want)
	}
}

// A PID that cannot be opened is unavailable with a reason. Reporting 0 bytes
// would read as "it stayed tiny" in exactly the case where the process is gone
// and nothing was measured.
func TestWindowsPeakRSSReportsUnavailableInsteadOfZero(t *testing.T) {
	peak := windowsPeakRSS(0x7ffffff0)
	if peak.Available {
		t.Fatalf("peak = %+v, want unavailable", peak)
	}
	if peak.Bytes != 0 {
		t.Fatalf("unavailable reading carried %d bytes", peak.Bytes)
	}
	if peak.Reason == "" {
		t.Fatal("unavailable reading has no reason")
	}
}

// The job object is what makes a tool-spawned PoC process visible: it is a
// grandchild of the scan node, never a direct child of saker. This asserts the
// sample path finds a job member by membership, not by executable name.
func TestChildPeakRSSIncludesJobMembers(t *testing.T) {
	if os.Getenv("SAKER_PEAK_HELPER") == "1" {
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestChildPeakRSSIncludesJobMembers")
	cmd.Env = append(os.Environ(), "SAKER_PEAK_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	if err := guardChild(cmd); err != nil {
		t.Skipf("this Windows host refuses job assignment (nested job): %v", err)
	}

	// roots is empty on purpose: only job membership can find the helper.
	peaks := childPeakRSS(nil)
	for _, p := range peaks {
		if p.PID != cmd.Process.Pid {
			continue
		}
		if !p.Available || p.Bytes == 0 {
			t.Fatalf("job member %d: %+v, want an available non-zero peak", p.PID, p)
		}
		return
	}
	t.Fatalf("pid %d is in the job but missing from %+v", cmd.Process.Pid, peaks)
}
