package main

// guardBareInvocation stops a bare invocation (no arguments at all) from
// starting the control plane. A session that runs `"$SAKER_BIN"` with no
// subcommand expects usage text; instead the server flags used to fall through
// and the process would start a listener and block (a real run burned the tool's
// full 600s timeout this way). Any real start passes at least one flag
// (-addr/-data/...), so len(args)==1 is exactly the accidental case.
//
// usage and exit are injected so the guard can be tested without spawning a
// process: the test asserts usage runs and exit(2) is requested, which is what
// keeps the server from ever being built.
func guardBareInvocation(args []string, usage func(), exit func(int)) bool {
	if len(args) != 1 {
		return false
	}
	usage()
	exit(2)
	return true
}
