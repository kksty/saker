//go:build linux

package host

import (
	"os/exec"
	"syscall"
	"testing"
)

// The supervised host must be armed before exec: Pdeathsig is taken from the
// forking thread and cannot be set by guardChild after cmd.Start. This test
// pins both halves of the Linux stand-in for the Windows job object — the
// process group (one signal takes the tree) and the parent-death signal (a
// killed saker still takes the host down).
func TestSetProcAttrArmsTheParentDeathSignal(t *testing.T) {
	cmd := exec.Command("true")
	setProcAttr(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("setProcAttr left SysProcAttr nil")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Fatalf("Setpgid = false, want the host in its own group")
	}
	if cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatalf("Pdeathsig = %v, want SIGKILL", cmd.SysProcAttr.Pdeathsig)
	}
}

// The variant is only about the parent-death policy; the group must survive it,
// because killGroup sends one signal to -pgid and would otherwise hit saker itself.
func TestSetProcAttrKeepsOnlyTheGroupAndTheDeathSignal(t *testing.T) {
	cmd := exec.Command("true")
	setProcAttr(cmd)
	if cmd.SysProcAttr.Setsid {
		t.Fatal("Setsid must stay false: the child would leave saker's terminal and the group")
	}
	if cmd.SysProcAttr.Credential != nil {
		t.Fatalf("Credential = %+v, want nil (no account switch)", cmd.SysProcAttr.Credential)
	}
}
