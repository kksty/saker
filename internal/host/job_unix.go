//go:build !windows

package host

import "os/exec"

// guardChild is a no-op off Windows: killGroup puts each child in its own
// process group, and a graceful stop kills the group explicitly. Linux arms a
// parent-death signal in setProcAttr (setDeathSignal, deathsignal_linux.go) so a
// `kill -9 saker` still takes the host down; other Unixes have no such signal and
// rely on that stop path plus the orphan sweep at the next start. guardChild
// runs after cmd.Start, which is too late for Pdeathsig, so it stays empty here.
func guardChild(*exec.Cmd) error { return nil }
