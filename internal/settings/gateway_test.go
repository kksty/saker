package settings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSettingsViewShowsSavedGatewayWithoutCredentials(t *testing.T) {
	// Runtime projections already carry the gateway section; the console now
	// edits it, so the view must expose the instance shape (names included) for
	// the editor while never echoing key material.
	store := guardrailStore(t, `gateway:
  private-instance:
    key: gateway-secret
  disabled:
    enabled: false
  enabled:
    enabled: true
cyberhub:
  proxy: http://proxy:proxy-secret@fixture.test
`)
	gateway := store.View().Gateway
	if gateway.Instances != 3 || gateway.Enabled != 2 || !gateway.ProxyConfigured || !gateway.Editable {
		t.Fatalf("gateway = %+v", gateway)
	}
	for _, want := range []string{"private-instance", "gateway:", "***"} {
		if !strings.Contains(gateway.Document, want) {
			t.Fatalf("document lacks %q:\n%s", want, gateway.Document)
		}
	}
	body, err := json.Marshal(store.View())
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"gateway-secret", "proxy-secret"} {
		if strings.Contains(string(body), private) {
			t.Fatalf("settings view exposed %q", private)
		}
	}
}

func TestGatewayViewReportsInvalidEnabled(t *testing.T) {
	// A string boolean is rejected by the harness and must not count as enabled.
	// The document still renders so the operator can fix the flag in place.
	store := guardrailStore(t, "gateway:\n  fixture:\n    enabled: 'false'\n")
	view := store.View().Gateway
	if view.Reason == "" || view.Instances != 0 || !view.Editable {
		t.Fatalf("unexpected gateway state: %+v", view)
	}
	if !strings.Contains(view.Document, "enabled: 'false'") {
		t.Fatalf("editor lost the invalid flag:\n%s", view.Document)
	}
}

func TestGatewayViewReportsUneditableShape(t *testing.T) {
	// A non-mapping section cannot be shown as an instance editor; the view says
	// so instead of offering a save that would replace it blindly.
	store := guardrailStore(t, "gateway: not-a-mapping\n")
	view := store.View().Gateway
	if view.Editable || view.Reason == "" {
		t.Fatalf("gateway = %+v", view)
	}
	if view.Document != "" {
		t.Fatalf("unreadable section still rendered: %q", view.Document)
	}
}

func TestGatewayCredentialsAreRedacted(t *testing.T) {
	// Gateway credentials use names such as key and url, so the existing
	// api_key walker missed them when a message contained the stored value.
	store := guardrailStore(t, `gateway:
  fixture:
    key: fixture-gateway-secret
    url: https://fixture.test/private-token
    proxy: http://fixture:private-token@fixture.test
`)
	input := "fixture-gateway-secret https://fixture.test/private-token http://fixture:private-token@fixture.test"
	if got := store.redactSecrets(input); got != "*** *** ***" {
		t.Fatalf("gateway credentials escaped redaction: %q", got)
	}
}
