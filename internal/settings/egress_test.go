package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func loadEgressStore(t *testing.T, document string) *Store {
	t.Helper()
	dir := t.TempDir()
	harness := filepath.Join(dir, "cyber.yaml")
	if err := os.WriteFile(harness, []byte(document), 0o600); err != nil {
		t.Fatalf("write cyber.yaml: %v", err)
	}
	st, err := LoadAt(filepath.Join(dir, "settings.json"), harness)
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	return st
}

// A selected clash instance is a subscription, and the view must not echo the
// subscription URL: the operator sees the instance name, the secret stays in
// the document.
func TestEgressViewReportsSelectedClashInstance(t *testing.T) {
	st := loadEgressStore(t, `gateway:
  office:
    kind: clash
    url: https://sub.example/secret-token
    refresh: 30m
    strategy: adaptive
  fofa:
    base_url: https://fofa.example
    key: fixture-key
cyberhub:
  proxy: office
  mitm: false
llm:
  api_key: fixture-llm-key
`)
	view := st.EgressView()
	if view.Mode != "subscription" || !view.Configured || view.Egress != "office" || view.Capture {
		t.Fatalf("view = %+v, want subscription/office/capture=false", view)
	}
	if view.Invalid != "" {
		t.Fatalf("unexpected invalid: %s", view.Invalid)
	}

	probeDoc, err := st.EgressProbeDocument()
	if err != nil {
		t.Fatalf("EgressProbeDocument: %v", err)
	}
	var decoded map[string]map[string]any
	if err := yaml.Unmarshal(probeDoc, &decoded); err != nil {
		t.Fatalf("probe document is not YAML: %v\n%s", err, probeDoc)
	}
	if decoded["cyberhub"]["proxy"] != "office" || decoded["cyberhub"]["mitm"] != false {
		t.Fatalf("probe cyberhub = %v", decoded["cyberhub"])
	}
	if _, ok := decoded["gateway"]["office"]; !ok {
		t.Fatalf("probe document dropped the clash instance:\n%s", probeDoc)
	}
	// The probe process must not receive credentials or sections it never
	// needs: the scan would load them, and a probe is not a place to hold a key.
	for _, forbidden := range []string{"fofa", "fixture-key", "fixture-llm-key", "llm:"} {
		if strings.Contains(string(probeDoc), forbidden) {
			t.Fatalf("probe document contains %q:\n%s", forbidden, probeDoc)
		}
	}
}

func TestEgressViewMasksProxyCredentialsAndDefaultsToDirect(t *testing.T) {
	masked := loadEgressStore(t, "cyberhub:\n  proxy: http://user:pass@127.0.0.1:7890\n")
	view := masked.EgressView()
	if view.Mode != "single" || view.Egress != "http://***@127.0.0.1:7890" {
		t.Fatalf("view = %+v, want single with masked userinfo", view)
	}
	if strings.Contains(view.Egress, "pass") || strings.Contains(view.Egress, "user") {
		t.Fatalf("view leaked credentials: %s", view.Egress)
	}

	direct := loadEgressStore(t, "gateway: {}\n")
	if view := direct.EgressView(); view.Mode != "direct" || view.Configured || !view.Capture {
		t.Fatalf("direct view = %+v, want direct/not configured/capture=true", view)
	}
}

// Two clash instances without a selection make the scan refuse to start: the
// page has to say that, not "configured".
func TestEgressViewReportsAmbiguousClashSelection(t *testing.T) {
	st := loadEgressStore(t, `gateway:
  a:
    kind: clash
    url: https://a.example/sub
  b:
    kind: clash
    url: https://b.example/sub
`)
	view := st.EgressView()
	if view.Invalid == "" || !strings.Contains(view.Invalid, "cyberhub.proxy") {
		t.Fatalf("view = %+v, want an ambiguity reason", view)
	}
	if !view.Refused {
		t.Fatalf("view = %+v, want Refused: the scan exits before it can dial", view)
	}
}

// A disabled instance is still a match for the gateway's "at most one service"
// rule: with two clash instances (one disabled) and no selection the scan exits
// with "select a clash service from [a b]", so the page must not claim the one
// enabled instance is in use. The probe document must carry the disabled
// instance too, or the probe would start a scan the real config cannot.
func TestEgressViewCountsDisabledClashInstancesAsSelectionCandidates(t *testing.T) {
	st := loadEgressStore(t, `gateway:
  a:
    kind: clash
    url: https://a.example/sub
  b:
    kind: clash
    url: https://b.example/sub
    enabled: false
`)
	view := st.EgressView()
	if view.Invalid == "" || !strings.Contains(view.Invalid, "a、b") {
		t.Fatalf("view = %+v, want both instances named in the ambiguity", view)
	}
	if !view.Refused {
		t.Fatalf("view = %+v, want Refused for the ambiguous selection", view)
	}
	doc, err := st.EgressProbeDocument()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "b:") || !strings.Contains(string(doc), "enabled: false") {
		t.Fatalf("probe document dropped the disabled instance:\n%s", doc)
	}
}

// "Select office" is only an egress if office is loaded. The scan accepts a
// disabled instance by name and then skips it, so the effective egress is
// direct; reporting "subscription · office" would describe a proxy that is not
// there. The reason travels on the invalid line rather than as a silent direct.
func TestEgressViewReportsSelectionOfDisabledInstance(t *testing.T) {
	st := loadEgressStore(t, `gateway:
  office:
    kind: clash
    url: https://office.example/sub
    enabled: false
cyberhub:
  proxy: office
`)
	view := st.EgressView()
	if view.Mode != "direct" || view.Configured || view.Egress != "" {
		t.Fatalf("view = %+v, want the effective egress to be direct", view)
	}
	if !strings.Contains(view.Invalid, "停用") || !strings.Contains(view.Invalid, "office") {
		t.Fatalf("invalid = %q, want the disabled selection named", view.Invalid)
	}
	if view.Refused {
		t.Fatalf("view = %+v, a disabled selection starts the scan (no proxy) and is still measurable", view)
	}
}

// The single auto-selected instance being disabled is not an error: the scan
// starts, loads no proxy, and traffic goes direct. Saying "subscription" here
// would invent an egress.
func TestEgressViewSingleDisabledInstanceIsDirect(t *testing.T) {
	st := loadEgressStore(t, `gateway:
  office:
    kind: clash
    url: https://office.example/sub
    enabled: false
`)
	view := st.EgressView()
	if view.Mode != "direct" || view.Configured || view.Invalid != "" {
		t.Fatalf("view = %+v, want a plain direct egress", view)
	}
	if view.Refused {
		t.Fatalf("view = %+v, the scan starts with a single disabled instance", view)
	}
}
