package settings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSettingsViewShowsSavedGatewayWithoutCredentials(t *testing.T) {
	// Runtime projections already carry the gateway section, but settings
	// previously had no state for it or its proxy selector. Check the public
	// view through a saved file, including an omitted and explicit enabled flag.
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
	view := store.View()
	if want := (GatewayView{Instances: 3, Enabled: 2, ProxyConfigured: true}); view.Gateway != want {
		t.Fatalf("gateway = %+v, want %+v", view.Gateway, want)
	}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-instance", "gateway-secret", "proxy-secret"} {
		if strings.Contains(string(body), private) {
			t.Fatalf("settings view exposed %q", private)
		}
	}
}

func TestGatewayViewReportsInvalidEnabled(t *testing.T) {
	// A string boolean is rejected by the harness and must not count as enabled.
	store := guardrailStore(t, "gateway:\n  fixture:\n    enabled: 'false'\n")
	view := store.View().Gateway
	if view.Reason == "" || view.Instances != 0 {
		t.Fatalf("unexpected gateway state: %+v", view)
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
