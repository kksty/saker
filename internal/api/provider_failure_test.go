package api

import (
	"testing"

	"github.com/kksty/saker/internal/executor"
	"github.com/kksty/saker/internal/model"
)

// The provider-failure classifier is the only thing that decides "this failed
// run was the gateway rejecting us", so its classes and the pause boundary are
// pinned here: only auth/quota rejection pauses the project, while rate limiting
// and transient errors must not (the external gateway owns backoff/rotation).
func TestClassifyProviderFailureClasses(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   providerFailure
	}{
		{"auth 401", "API error (401): invalid_api_key", providerFailureAuth},
		{"auth 403", "API error (403): permission denied", providerFailureAuth},
		{"quota 402", "API error (402): Insufficient Balance", providerFailureQuota},
		{"quota wording", "额度已用尽", providerFailureQuota},
		{"rate 429", "API error (429): too many requests", providerFailureRate},
		{"transient 5xx", "API error (500): upstream error", providerFailureTransient},
		{"transient network", "dial tcp: connection refused", providerFailureTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyProviderFailure(executor.Result{IsError: true, Output: tc.output, StopReason: "error"})
			if got != tc.want {
				t.Fatalf("class = %s, want %s", got, tc.want)
			}
			wantPause := tc.want == providerFailureAuth || tc.want == providerFailureQuota
			if got.requiresOperator() != wantPause {
				t.Fatalf("requiresOperator = %t, want %t", got.requiresOperator(), wantPause)
			}
		})
	}
}

func TestClassifyProviderFailureIgnoresSuccessfulRun(t *testing.T) {
	got := classifyProviderFailure(executor.Result{Output: "API error (401): invalid_api_key", StopReason: "completed"})
	if got != providerFailureNone {
		t.Fatalf("successful run classified as %s", got)
	}
}

// A rejected gateway pauses the project so no further round burns on a dead
// credential; a rate limit must not pause, because the key is still good.
func TestProviderFailurePauseBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		pauses bool
	}{
		{"auth", "API error (401): invalid_api_key", true},
		{"quota", "API error (402): Insufficient Balance", true},
		{"rate", "API error (429): rate limit", false},
		{"transient", "API error (500): upstream error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkerFixture(t)
			f.srv.afterProviderFailure("", "", model.RunFailed, executor.Result{IsError: true, Output: tc.output, StopReason: "error"})
			if got := f.srv.paused.Load(); got != tc.pauses {
				t.Fatalf("paused = %t, want %t", got, tc.pauses)
			}
		})
	}
}
