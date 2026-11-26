package main

import "testing"

// A bare invocation prints usage and asks for a non-zero exit, so the server
// code below the guard is never reached.
func TestBareInvocationPrintsUsageAndExits(t *testing.T) {
	var used bool
	var code int = -1
	if !guardBareInvocation([]string{"saker"}, func() { used = true }, func(c int) { code = c }) {
		t.Fatal("bare invocation was not guarded: it would fall through to the server")
	}
	if !used {
		t.Fatal("bare invocation did not print usage")
	}
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

// Any real start passes a flag, and must not be intercepted.
func TestStartWithFlagsIsNotGuarded(t *testing.T) {
	called := false
	usage := func() { called = true }
	exit := func(int) { called = true }
	for _, args := range [][]string{
		{"saker", "-addr", "127.0.0.1:8080"},
		{"saker", "-data", "./data"},
		{"saker", "-version"},
		{"saker", "-h"},
	} {
		if guardBareInvocation(args, usage, exit) {
			t.Fatalf("%v was treated as a bare invocation", args)
		}
	}
	if called {
		t.Fatal("a flagged start printed usage or exited")
	}
}
