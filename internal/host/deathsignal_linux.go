//go:build linux

package host

import (
	"os/exec"
	"syscall"
)

// setDeathSignal asks the kernel to SIGKILL this child when the thread that
// forked it exits. It is the Linux stand-in for the Windows job object: without
// it, `kill -9 saker` (or any crash) leaves hub/scan running — holding the AOP
// port and spending model tokens — until the next start's orphan sweep. Windows
// gets the stronger guarantee from JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
// (job_windows.go).
//
// Two limits are inherent; do not read this as "Linux is now equivalent":
//   - only the child itself dies. A shell or tool that child started is not
//     followed, because there is no cgroup here; Windows' job follows the tree.
//   - the kernel ties the signal to the forking thread, not the process. Go does
//     not normally retire OS threads, but a locked thread that exited early
//     would fire it while saker is still alive. Nothing here locks that thread.
//
// It must be armed before exec: execLaunch calls setProcAttr before
// cmd.Start. guardChild runs after Start (Windows assigns the job there), so
// this cannot live in guardChild.
func setDeathSignal(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
