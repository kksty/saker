package settings

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func readStoreFile(t *testing.T, s *Store) string {
	t.Helper()
	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The console edits the gateway section as YAML. Reading must show the shape so
// an instance can be added or disabled, while the harness's secret fields stay
// masked: key/url/proxy and a proxy URL that embeds userinfo.
func TestGatewayDocumentMasksSecretFields(t *testing.T) {
	store := guardrailStore(t, `gateway:
  fofa:
    base_url: https://old.test/fofa
    key: fixture-gateway-key
  office:
    kind: clash
    url: https://subscription.test/private-token
    refresh: 30m
  disabled:
    enabled: false
cyberhub:
  proxy: http://user:proxy-password@proxy.test
`)
	view := store.View().Gateway
	if !view.Editable || view.Instances != 3 || view.Enabled != 2 || !view.ProxyConfigured {
		t.Fatalf("gateway view = %+v", view)
	}
	for _, want := range []string{"gateway:", "fofa:", "https://old.test/fofa", "office:", "kind: clash", "enabled: false", "cyberhub:", "***"} {
		if !strings.Contains(view.Document, want) {
			t.Fatalf("document lacks %q:\n%s", want, view.Document)
		}
	}
	for _, secret := range []string{"fixture-gateway-key", "private-token", "proxy-password"} {
		if strings.Contains(view.Document, secret) {
			t.Fatalf("document leaked %q:\n%s", secret, view.Document)
		}
	}
}

func TestSaveGatewayKeepsMaskedSecretsAndUnrelatedSections(t *testing.T) {
	store := guardrailStore(t, `gateway:
  fofa:
    base_url: https://old.test/fofa
    key: fixture-gateway-key
  office:
    kind: clash
    url: https://subscription.test/private-token
cyberhub:
  proxy: office
mcp:
  mcpServers:
    local:
      command: npx
`)
	document := store.View().Gateway.Document
	edited := strings.Replace(document, "https://old.test/fofa", "https://new.test/fofa", 1)
	if edited == document {
		t.Fatalf("fixture document did not carry the address:\n%s", document)
	}
	changed, err := store.SaveGateway(GatewayInput{Document: edited}, nil)
	if err != nil || !changed {
		t.Fatalf("save = %v %v", changed, err)
	}
	saved := readStoreFile(t, store)
	for _, want := range []string{
		"https://new.test/fofa", "fixture-gateway-key",
		"https://subscription.test/private-token", "proxy: office",
		"mcpServers:",
	} {
		if !strings.Contains(saved, want) {
			t.Fatalf("saved file lacks %q:\n%s", want, saved)
		}
	}
	if strings.Contains(saved, maskedValue) {
		t.Fatalf("masked placeholder was written to disk:\n%s", saved)
	}
}

func TestSaveGatewayRemovesInstancesAndClearsProxy(t *testing.T) {
	store := guardrailStore(t, `gateway:
  fofa:
    base_url: https://old.test/fofa
    key: fixture-key
  office:
    kind: clash
    url: https://subscription.test/token
cyberhub:
  proxy: office
`)
	changed, err := store.SaveGateway(GatewayInput{Document: "gateway:\n  fofa:\n    base_url: https://new.test/fofa\n    key: '***'\n"}, nil)
	if err != nil || !changed {
		t.Fatalf("save = %v %v", changed, err)
	}
	saved := readStoreFile(t, store)
	if strings.Contains(saved, "\n  office:") {
		t.Fatalf("removed instance is still stored:\n%s", saved)
	}
	if strings.Contains(saved, "proxy:") {
		t.Fatalf("cleared proxy is still stored:\n%s", saved)
	}
	if !strings.Contains(saved, "https://new.test/fofa") || !strings.Contains(saved, "fixture-key") {
		t.Fatalf("saved file lost the edited instance:\n%s", saved)
	}
}

// An untouched round trip must not rewrite the operator's file: the panel is
// opened and saved without edits, or a stale draft is posted twice.
func TestSaveGatewayWithoutChangeDoesNotRewrite(t *testing.T) {
	store := guardrailStore(t, `gateway:
  fofa:
    key: fixture-key
cyberhub:
  proxy: office
`)
	before := readStoreFile(t, store)
	changed, err := store.SaveGateway(GatewayInput{Document: store.View().Gateway.Document}, nil)
	if err != nil || changed {
		t.Fatalf("no-op save = %v %v", changed, err)
	}
	if after := readStoreFile(t, store); after != before {
		t.Fatalf("no-op save rewrote the file:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// The scan's own validator is the authority on what the host accepts; it must
// run on the masked-then-restored candidate and its rejection must leave the
// saved file alone.
func TestSaveGatewayValidatesRestoredCandidateBeforeWriting(t *testing.T) {
	store := guardrailStore(t, "gateway:\n  fofa:\n    key: stored-key\n")
	before := readStoreFile(t, store)
	var candidate []byte
	_, err := store.SaveGateway(GatewayInput{Document: "gateway:\n  fofa:\n    base_url: https://new.test\n    key: '***'\n"}, func(sections []byte) error {
		candidate = sections
		return errors.New(`extension gateway: gateway.fofa: unknown kind "nope"`)
	})
	if !errors.Is(err, ErrGatewayConfig) || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("validator rejection = %v", err)
	}
	if !bytes.Contains(candidate, []byte("stored-key")) {
		t.Fatalf("validator saw the mask instead of the stored key:\n%s", candidate)
	}
	if bytes.Contains(candidate, []byte(maskedValue)) {
		t.Fatalf("validator candidate carried the mask:\n%s", candidate)
	}
	if after := readStoreFile(t, store); after != before {
		t.Fatalf("rejected save rewrote the file:\n%s", after)
	}
}

func TestSaveGatewayRejectsInvalidDocuments(t *testing.T) {
	for name, document := range map[string]string{
		"empty":                  "",
		"unknown top section":    "guardrail:\n  provider: jev\n",
		"gateway not a map":      "gateway: 1\n",
		"instance not a map":     "gateway:\n  fofa: 1\n",
		"invalid instance name":  "gateway:\n  'bad name':\n    enabled: true\n",
		"string enabled":         "gateway:\n  fofa:\n    enabled: 'false'\n",
		"non-string kind":        "gateway:\n  fofa:\n    kind: 1\n",
		"nested field value":     "gateway:\n  fofa:\n    base_url:\n      scheme: https\n",
		"duplicate field":        "gateway:\n  fofa:\n    key: a\n    key: b\n",
		"duplicate instance":     "gateway:\n  fofa:\n    key: a\n  fofa:\n    key: b\n",
		"two documents":          "gateway: {}\n---\ngateway: {}\n",
		"unknown cyberhub field": "cyberhub:\n  url: https://hub.test\n",
		"proxy not a string":     "cyberhub:\n  proxy: [a]\n",
		"mask without stored":    "gateway:\n  fofa:\n    key: '***'\n",
	} {
		t.Run(name, func(t *testing.T) {
			store := guardrailStore(t, "gateway:\n  fixture:\n    key: stored\n")
			before := readStoreFile(t, store)
			if _, err := store.SaveGateway(GatewayInput{Document: document}, nil); !errors.Is(err, ErrGatewayConfig) {
				t.Fatalf("err = %v, want ErrGatewayConfig", err)
			}
			if after := readStoreFile(t, store); after != before {
				t.Fatalf("rejected document rewrote the file:\n%s", after)
			}
		})
	}
}

// A legacy `extensions:` envelope is promoted by the write path, like every
// other settings save.
func TestSaveGatewayMigratesLegacyExtensionsEnvelope(t *testing.T) {
	store := guardrailStore(t, `extensions:
  gateway:
    fofa:
      key: legacy-key
`)
	document := store.View().Gateway.Document
	if !strings.Contains(document, "fofa:") {
		t.Fatalf("legacy gateway was not rendered:\n%s", document)
	}
	edited := "gateway:\n  fofa:\n    key: '***'\n  hunter:\n    key: hunter-key\ncyberhub:\n  proxy: ''\n"
	changed, err := store.SaveGateway(GatewayInput{Document: edited}, nil)
	if err != nil || !changed {
		t.Fatalf("save = %v %v", changed, err)
	}
	saved := readStoreFile(t, store)
	if strings.Contains(saved, "extensions:") {
		t.Fatalf("legacy envelope survived the save:\n%s", saved)
	}
	if !strings.Contains(saved, "legacy-key") || !strings.Contains(saved, "hunter-key") {
		t.Fatalf("saved file lost instance credentials:\n%s", saved)
	}
}
