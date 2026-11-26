//go:build windows

package host

import (
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The point of the job object is that the OS kills the children when the job
// handle closes — no signal from us, no cleanup code of ours running. That is
// exactly the case an exe swap creates: saker is killed with taskkill /F, so the
// only thing that can stop the Hub and scan node is the job.
func TestClosingTheJobKillsAssignedChildren(t *testing.T) {
	if os.Getenv("SAKER_JOB_HELPER") == "1" {
		// Child mode: live until the OS kills us.
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}

	job, err := newJobHandle()
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = windows.CloseHandle(job)
		}
	}()

	cmd := exec.Command(os.Args[0], "-test.run=TestClosingTheJobKillsAssignedChildren")
	cmd.Env = append(os.Environ(), "SAKER_JOB_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if err := assignToJob(job, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}

	// Absence window: while the job is open the child must stay alive, otherwise
	// this test would pass for the wrong reason (a child that died on its own).
	select {
	case <-done:
		t.Fatal("child exited before the job was closed")
	case <-time.After(300 * time.Millisecond):
	}

	if err := windows.CloseHandle(job); err != nil {
		t.Fatal(err)
	}
	closed = true

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child survived the job handle close")
	}
}

// guardChild is what the real launch path calls. This asserts the child actually
// appears in the process-wide job: if assignment silently failed, the leak would
// be back and nothing else in the suite would notice.
func TestGuardChildAssignsIntoTheProcessJob(t *testing.T) {
	if os.Getenv("SAKER_JOB_HELPER") == "1" {
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestGuardChildAssignsIntoTheProcessJob")
	cmd.Env = append(os.Environ(), "SAKER_JOB_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	if err := guardChild(cmd); err != nil {
		t.Fatalf("guardChild: %v", err)
	}

	job, err := childJob()
	if err != nil {
		t.Fatal(err)
	}
	info := jobBasicAccounting{}
	var ret uint32
	if err := windows.QueryInformationJobObject(job, jobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &ret); err != nil {
		t.Skipf("cannot query the job on this Windows build: %v", err)
	}
	if info.ActiveProcesses == 0 {
		t.Fatal("the child was not assigned to the process job")
	}
}

// JOBOBJECT_BASIC_ACCOUNTING_INFORMATION is not in x/sys v0.48.0; the layout is
// the documented one (four LARGE_INTEGERs then four DWORDs). Only
// ActiveProcesses is read here.
type jobBasicAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

const jobObjectBasicAccountingInformation = 1
