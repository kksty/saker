//go:build windows

package host

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A Windows child does not die with its parent: if `saker` is killed (even with
// taskkill /F, which is exactly how an exe swap stops it), the Hub and scan node
// keep running and keep listening. The user's requirement is the opposite — when
// saker goes away, its own children go with it.
//
// The only mechanism that survives a forced kill of the parent is a Job Object
// with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE: the job handle is owned by this
// process, and the OS terminates every assigned process when that handle is
// closed — which happens on normal exit, on taskkill, and on a crash alike.
//
// The handle is deliberately never closed by our code: the OS closes it as the
// process dies, which is what triggers the kill. Closing it earlier would kill
// the Hub while the control plane is still running.
var (
	jobOnce   sync.Once
	jobHandle windows.Handle
	jobErr    error
)

func childJob() (windows.Handle, error) {
	jobOnce.Do(func() {
		h, err := newJobHandle()
		if err != nil {
			jobErr = err
			return
		}
		jobHandle = h
	})
	return jobHandle, jobErr
}

// newJobHandle creates one job that kills its members when the handle closes.
func newJobHandle() (windows.Handle, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

func assignToJob(job windows.Handle, pid int) error {
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ph)
	return windows.AssignProcessToJobObject(job, ph)
}

// guardChild puts a freshly started child into the process-wide job. A failure is
// reported by the caller's log rather than swallowed: without the job, the child
// outlives its parent, and that is the bug this file exists to prevent.
func guardChild(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	job, err := childJob()
	if err != nil {
		return err
	}
	return assignToJob(job, cmd.Process.Pid)
}
