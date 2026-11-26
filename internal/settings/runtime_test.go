package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRuntimeConfigUsesOnlySingleConnectionAndPreservesLegacySource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "cyber.yaml")
	document := `llm:
  active_profile: chosen
  providers:
    - id: chosen
      provider: openai
      base_url: http://gateway.test/v1
      model: gateway-route
      api_key: gateway-key
      api_keys: [retired-spare]
      tier: strong
      fallback: retired
    - id: retired
      base_url: http://retired.test/v1
      model: retired-model
      api_key: retired-key
agent:
  timeout: 60
`
	if err := os.WriteFile(source, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadAt(filepath.Join(dir, "settings.json"), source)
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.RuntimeConfig(filepath.Join(dir, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"providers:", "active_profile:", "api_keys:", "tier:", "fallback:", "retired"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("runtime kept %s: %s", forbidden, data)
		}
	}
	for _, want := range []string{"gateway-route", "gateway-key", "http://gateway.test/v1", "timeout: 60"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s", want)
		}
	}
	original, _ := os.ReadFile(source)
	if string(original) != document {
		t.Fatal("reading runtime mutated operator configuration")
	}
	same, err := s.RuntimeConfig(filepath.Join(dir, "runtime"))
	if err != nil || same != path {
		t.Fatal("unchanged runtime configuration changed path")
	}
	for _, secret := range []string{"gateway-key", "retired-key", "retired-spare"} {
		found := false
		for _, v := range s.SecretValues() {
			if v == secret {
				found = true
			}
		}
		if !found {
			t.Fatalf("historic secret not redacted: %s", secret)
		}
	}
}
func TestSingleConnectionDoesNotSelectSpareKeys(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "cyber.yaml")
	if err := os.WriteFile(source, []byte("llm:\n  active_profile: old\n  providers:\n    - id: old\n      base_url: http://gateway.test/v1\n      model: route\n      api_keys: [spare-one, spare-two]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadAt(filepath.Join(dir, "settings.json"), source)
	if err != nil {
		t.Fatal(err)
	}
	if s.Effective().APIKey != "" {
		t.Fatal("removed key pool still supplies a credential")
	}
}

// runtimeDocument is the generated host runtime config parsed as a plain YAML
// map: the assertions are about the document the harness will read, not about
// the node-graph helpers that produced it.
func runtimeDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("runtime config 不是合法 YAML：%v\n%s", err, data)
	}
	return doc
}

func runtimeSection(t *testing.T, doc map[string]any, key string) map[string]any {
	t.Helper()
	raw, ok := doc[key]
	if !ok {
		t.Fatalf("runtime config 缺少 %s 段：%v", key, doc)
	}
	section, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("runtime config 的 %s 段不是映射：%T", key, raw)
	}
	return section
}

func runtimeInt(t *testing.T, section map[string]any, key string) int64 {
	t.Helper()
	switch v := section[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case uint64:
		return int64(v)
	default:
		t.Fatalf("%s 不是整数：%T (%v)", key, section[key], section[key])
		return 0
	}
}

func writeOperatorConfig(t *testing.T, dir, document string) string {
	t.Helper()
	source := filepath.Join(dir, "cyber.yaml")
	if err := os.WriteFile(source, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	return source
}

func generateRuntime(t *testing.T, source string) string {
	t.Helper()
	s, err := LoadAt(filepath.Join(filepath.Dir(source), "settings.json"), source)
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.RuntimeConfig(filepath.Join(filepath.Dir(source), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// The harness's own default for traffic.body_storage is "none", which keeps only
// truncated previews. The console's traffic page needs the original bytes, so
// the runtime config has to switch the local scan store to disk while keeping
// the harness's limits bounded.
func TestRuntimeConfigWritesBoundedTrafficDefaults(t *testing.T) {
	dir := t.TempDir()
	source := writeOperatorConfig(t, dir, `llm:
  active_profile: chosen
  providers:
    - id: chosen
      provider: openai
      base_url: http://gateway.test/v1
      model: gateway-route
      api_key: gateway-key
`)
	doc := runtimeDocument(t, generateRuntime(t, source))
	traffic := runtimeSection(t, doc, "traffic")
	if got := traffic["body_storage"]; got != "disk" {
		t.Fatalf("body_storage = %v, want disk", got)
	}
	if got := runtimeInt(t, traffic, "body_max_bytes"); got != 8388608 {
		t.Fatalf("body_max_bytes = %d, want 8388608", got)
	}
	if got := runtimeInt(t, traffic, "body_retention_bytes"); got != 2147483648 {
		t.Fatalf("body_retention_bytes = %d, want 2147483648", got)
	}
}

// A partially filled traffic section keeps the operator's explicit fields and
// only receives defaults for the keys they omitted: merging, not overwriting.
func TestRuntimeConfigFillsOnlyMissingTrafficKeys(t *testing.T) {
	dir := t.TempDir()
	source := writeOperatorConfig(t, dir, `llm:
  base_url: http://gateway.test/v1
  model: route
  api_key: key
traffic:
  body_max_bytes: 4096
`)
	doc := runtimeDocument(t, generateRuntime(t, source))
	traffic := runtimeSection(t, doc, "traffic")
	if got := runtimeInt(t, traffic, "body_max_bytes"); got != 4096 {
		t.Fatalf("explicit body_max_bytes overwritten: %d", got)
	}
	if got := traffic["body_storage"]; got != "disk" {
		t.Fatalf("missing body_storage not defaulted: %v", got)
	}
	if got := runtimeInt(t, traffic, "body_retention_bytes"); got != 2147483648 {
		t.Fatalf("missing body_retention_bytes not defaulted: %d", got)
	}
}

// Product sections (mcp, search, guardrail, jev) are copied through on purpose.
// They must live under `extensions`: the harness's strict file validator
// rejects an unknown top-level section on the Hub, while an extension section
// this host does not declare is preserved and diagnosed. internal/host reuses
// this same --config file for the local scan (Host.nodeArgs), so the scan
// resolves the sections from here. An explicit operator traffic value must win
// field by field.
func TestRuntimeConfigPreservesProductSectionsAndExplicitTraffic(t *testing.T) {
	dir := t.TempDir()
	source := writeOperatorConfig(t, dir, `llm:
  active_profile: chosen
  providers:
    - id: chosen
      base_url: http://gateway.test/v1
      model: gateway-route
      api_key: gateway-key
      tier: strong
agent:
  timeout: 60
  tools: [bash, browser]
traffic:
  body_storage: none
  body_max_bytes: 1048576
  body_retention_bytes: 4194304
extensions:
  mcp:
    mcpServers:
      browser:
        command: npx
        args: [browser-mcp]
  search:
    tavily_keys: search-key
  guardrail:
    provider: jev
    mode: auto
  jev:
    api_key: ""
`)
	path := generateRuntime(t, source)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := runtimeDocument(t, path)

	traffic := runtimeSection(t, doc, "traffic")
	if got := traffic["body_storage"]; got != "none" {
		t.Fatalf("operator's explicit body_storage overwritten: %v", got)
	}
	if got := runtimeInt(t, traffic, "body_max_bytes"); got != 1048576 {
		t.Fatalf("operator's explicit body_max_bytes overwritten: %d", got)
	}
	if got := runtimeInt(t, traffic, "body_retention_bytes"); got != 4194304 {
		t.Fatalf("operator's explicit body_retention_bytes overwritten: %d", got)
	}

	extensions, ok := doc["extensions"].(map[string]any)
	if !ok {
		t.Fatalf("extensions section was not passed through: %s", data)
	}
	for _, section := range []string{"mcp", "search", "guardrail", "jev"} {
		if _, ok := extensions[section].(map[string]any); !ok {
			t.Fatalf("product section extensions.%s was not passed through: %s", section, data)
		}
	}
	for _, topLevel := range []string{"mcp", "search", "guardrail", "jev"} {
		if _, ok := doc[topLevel]; ok {
			t.Fatalf("product section %q leaked to the rejected top level: %s", topLevel, data)
		}
	}
	if !strings.Contains(string(data), "npx") || !strings.Contains(string(data), "search-key") {
		t.Fatalf("product section values were not passed through: %s", data)
	}
	agent := runtimeSection(t, doc, "agent")
	if got := runtimeInt(t, agent, "timeout"); got != 60 {
		t.Fatalf("agent.timeout = %d, want 60", got)
	}

	// The LLM section stays a single explicit connection: the legacy provider
	// catalogue and its saker metadata never reach the harness.
	llm := runtimeSection(t, doc, "llm")
	for _, forbidden := range []string{"providers", "active_profile", "api_keys", "tier", "fallback"} {
		if _, ok := llm[forbidden]; ok {
			t.Fatalf("runtime llm section kept %q: %s", forbidden, data)
		}
	}
	if llm["model"] != "gateway-route" || llm["api_key"] != "gateway-key" {
		t.Fatalf("runtime llm section lost the active connection: %v", llm)
	}
}

// A legacy top-level product section is rejected by the harness during its own
// strict file validation, so the control plane names the mistake and the right
// location while the operator is editing settings instead of failing at the
// next scan startup.
func TestRuntimeConfigRejectsTopLevelProductSections(t *testing.T) {
	dir := t.TempDir()
	source := writeOperatorConfig(t, dir, `llm:
  base_url: http://gateway.test/v1
  model: route
  api_key: key
mcp:
  mcpServers:
    browser:
      command: npx
`)
	s, err := LoadAt(filepath.Join(dir, "settings.json"), source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RuntimeConfig(filepath.Join(dir, "runtime"))
	if err == nil || !strings.Contains(err.Error(), "extensions") {
		t.Fatalf("top-level mcp was not rejected with the extensions hint: %v", err)
	}
	if err := s.ValidateRuntimeConfig(); err == nil || !strings.Contains(err.Error(), "extensions") {
		t.Fatalf("save-time validation accepted a top-level product section: %v", err)
	}
}

// A traffic combination the harness would reject is reported by the control
// plane with the section named, instead of failing later inside scan startup.
func TestRuntimeConfigRejectsInvalidTrafficValues(t *testing.T) {
	for name, document := range map[string]string{
		"bad storage":       "traffic:\n  body_storage: memory\n",
		"negative limit":    "traffic:\n  body_max_bytes: -1\n",
		"retention too low": "traffic:\n  body_max_bytes: 8388608\n  body_retention_bytes: 1048576\n",
		"not a mapping":     "traffic: disk\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			source := writeOperatorConfig(t, dir, "llm:\n  base_url: http://gateway.test/v1\n  model: route\n  api_key: key\n"+document)
			s, err := LoadAt(filepath.Join(dir, "settings.json"), source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.RuntimeConfig(filepath.Join(dir, "runtime")); err == nil {
				t.Fatalf("invalid traffic config was written to the runtime document: %s", document)
			}
		})
	}
}
