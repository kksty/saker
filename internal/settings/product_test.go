package settings

import (
	"context"
	"encoding/json"
	"github.com/kksty/saker/internal/testutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func productStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cyber.yaml")
	body := `llm:
    active_profile: default
    providers:
        - id: default
          provider: openai
          base_url: http://gateway.test/v1
          model: route
          api_key: gateway-key
extensions:
    guardrail:
        provider: jev
        mode: auto
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadAt(filepath.Join(dir, "settings.json"), path)
	if err != nil {
		t.Fatal(err)
	}
	return st, path
}

func TestSaveProductWritesExtensionsAndKeepsOtherSections(t *testing.T) {
	st, path := productStore(t)
	servers := map[string]MCPServer{
		"ida": {Command: "idalib-mcp", Args: []string{"--stdio"}},
		"docs": {URL: "https://example.test/mcp", Headers: map[string]string{
			"Authorization": "Bearer secret",
		}},
	}
	if err := st.SaveProduct(ProductInput{MCPServers: &servers, SearchKey: "key-one,key-two"}); err != nil {
		t.Fatal(err)
	}
	product, err := st.Product()
	if err != nil {
		t.Fatal(err)
	}
	if got := product.MCPServers["ida"]; got.Command != "idalib-mcp" || len(got.Args) != 1 {
		t.Fatalf("ida = %+v", got)
	}
	if got := product.MCPServers["docs"]; got.URL != "https://example.test/mcp" || got.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("docs = %+v", got)
	}
	if product.SearchKeys != "key-one,key-two" {
		t.Fatalf("search keys = %q", product.SearchKeys)
	}

	// The operator document keeps its model and the unrelated guardrail section.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"\nmcp:", "\nsearch:", "mcpServers:", "idalib-mcp", "tavily_keys: key-one,key-two", "guardrail:", "provider: jev", "model: route"} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved config missing %q:\n%s", want, text)
		}
	}
	// The harness removed the xtensions: envelope; a save must keep writing
	// flat root sections.
	if strings.Contains(text, "extensions:") {
		t.Fatalf("product sections were wrapped in the removed extensions envelope:\n%s", text)
	}
}

func TestProductViewMasksSecretsAndSaveKeepsThem(t *testing.T) {
	st, _ := productStore(t)
	servers := map[string]MCPServer{
		"ida": {Command: "idalib-mcp", Env: map[string]string{"JADX_HOME": "C:/jadx", "TOKEN": "env-secret"}},
	}
	if err := st.SaveProduct(ProductInput{MCPServers: &servers, SearchKey: "tavily-secret"}); err != nil {
		t.Fatal(err)
	}
	view, err := st.ProductView()
	if err != nil {
		t.Fatal(err)
	}
	if got := view.MCPServers["ida"].Env["TOKEN"]; got != maskedValue {
		t.Fatalf("env value not masked: %q", got)
	}
	if !view.SearchKeySet || view.SearchKeyCount != 1 {
		t.Fatalf("search view = %+v", view)
	}
	// A console round-trip sends the masked map back; the stored secret must
	// survive instead of being written literally.
	roundTrip := map[string]MCPServer{
		"ida": {Command: "idalib-mcp", Env: map[string]string{"JADX_HOME": "D:/jadx", "TOKEN": maskedValue}},
	}
	if err := st.SaveProduct(ProductInput{MCPServers: &roundTrip}); err != nil {
		t.Fatal(err)
	}
	product, err := st.Product()
	if err != nil {
		t.Fatal(err)
	}
	if got := product.MCPServers["ida"].Env["TOKEN"]; got != "env-secret" {
		t.Fatalf("masked save overwrote the stored secret: %q", got)
	}
	if got := product.MCPServers["ida"].Env["JADX_HOME"]; got != "D:/jadx" {
		t.Fatalf("edited env value not saved: %q", got)
	}
	if product.SearchKeys != "tavily-secret" {
		t.Fatalf("an omitted search key was not kept: %q", product.SearchKeys)
	}
}

func TestSaveProductRejectsInvalidShapes(t *testing.T) {
	for name, servers := range map[string]map[string]MCPServer{
		"bad alias":            {"bad alias": {Command: "x"}},
		"neither command url":  {"x": {}},
		"both command and url": {"x": {Command: "x", URL: "http://example.test"}},
		"headers with command": {"x": {Command: "x", Headers: map[string]string{"h": "v"}}},
		"env with url":         {"x": {URL: "http://example.test", Env: map[string]string{"A": "B"}}},
		"non-http url":         {"x": {URL: "ftp://example.test"}},
		"negative timeout":     {"x": {Command: "x", TimeoutSeconds: -1}},
		"empty tool":           {"x": {Command: "x", Tools: []string{""}}},
	} {
		t.Run(name, func(t *testing.T) {
			st, _ := productStore(t)
			in := ProductInput{MCPServers: &servers}
			if err := st.SaveProduct(in); err == nil {
				t.Fatalf("invalid MCP config was accepted: %+v", servers)
			}
		})
	}
}

func TestSaveProductClearsSections(t *testing.T) {
	st, path := productStore(t)
	servers := map[string]MCPServer{"ida": {Command: "idalib-mcp"}}
	if err := st.SaveProduct(ProductInput{MCPServers: &servers, SearchKey: "k"}); err != nil {
		t.Fatal(err)
	}
	empty := map[string]MCPServer{}
	if err := st.SaveProduct(ProductInput{MCPServers: &empty, ClearSearchKey: true}); err != nil {
		t.Fatal(err)
	}
	product, err := st.Product()
	if err != nil {
		t.Fatal(err)
	}
	if len(product.MCPServers) != 0 || product.SearchKeys != "" {
		t.Fatalf("product after clear = %+v", product)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "idalib-mcp") || strings.Contains(string(data), "tavily_keys") {
		t.Fatalf("cleared product values remain:\n%s", data)
	}
	if !strings.Contains(string(data), "guardrail:") {
		t.Fatal("clearing product sections removed the unrelated guardrail section")
	}
}

func TestProductChangedDetectsOnlyRealChanges(t *testing.T) {
	st, _ := productStore(t)
	servers := map[string]MCPServer{"ida": {Command: "idalib-mcp", Env: map[string]string{"TOKEN": "secret"}}}
	if err := st.SaveProduct(ProductInput{MCPServers: &servers, SearchKey: "k1"}); err != nil {
		t.Fatal(err)
	}
	if changed, err := st.ProductChanged(ProductInput{}); err != nil || changed {
		t.Fatalf("empty save = %v %v", changed, err)
	}
	// A console round-trip sends the masked document back unchanged.
	masked, err := st.ProductView()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := st.ProductChanged(ProductInput{MCPServers: &masked.MCPServers}); err != nil || changed {
		t.Fatalf("masked round-trip reported a change: %v %v", changed, err)
	}
	// The key presence is not echoed by the view, so a save that omits it must
	// not count as removing it.
	if changed, err := st.ProductChanged(ProductInput{MCPServers: &masked.MCPServers, SearchKey: ""}); err != nil || changed {
		t.Fatalf("omitted search key reported a change: %v %v", changed, err)
	}
	edited := map[string]MCPServer{"ida": {Command: "idalib-mcp", Env: map[string]string{"TOKEN": maskedValue}, Args: []string{"--stdio"}}}
	if changed, err := st.ProductChanged(ProductInput{MCPServers: &edited}); err != nil || !changed {
		t.Fatalf("edited server = %v %v", changed, err)
	}
	if changed, err := st.ProductChanged(ProductInput{ClearSearchKey: true}); err != nil || !changed {
		t.Fatalf("cleared search key = %v %v", changed, err)
	}
	if err := st.ValidateProduct(ProductInput{MCPServers: &map[string]MCPServer{"bad alias": {Command: "x"}}}); err == nil {
		t.Fatal("ValidateProduct accepted a bad alias")
	}
}

// An explicit empty tool allowlist ("mount none") must not collapse into an
// omitted one ("mount everything") on a read/write round trip.
func TestProductPreservesExplicitEmptyToolAllowlist(t *testing.T) {
	st, path := productStore(t)
	servers := map[string]MCPServer{"ida": {Command: "idalib-mcp", Tools: []string{}}}
	if err := st.SaveProduct(ProductInput{MCPServers: &servers}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "tools: []") {
		t.Fatalf("explicit empty tools not written:\n%s", data)
	}
	product, err := st.Product()
	if err != nil {
		t.Fatal(err)
	}
	if product.MCPServers["ida"].Tools == nil {
		t.Fatal("explicit empty tools read back as the omitted state")
	}
	// The omitted state stays nil (mount everything).
	servers = map[string]MCPServer{"ida": {Command: "idalib-mcp"}}
	if err := st.SaveProduct(ProductInput{MCPServers: &servers}); err != nil {
		t.Fatal(err)
	}
	product, err = st.Product()
	if err != nil {
		t.Fatal(err)
	}
	if product.MCPServers["ida"].Tools != nil {
		t.Fatalf("omitted tools became explicit: %+v", product.MCPServers["ida"].Tools)
	}
}

func TestSaveProductRejectsMaskWithoutStoredValue(t *testing.T) {
	st, _ := productStore(t)
	servers := map[string]MCPServer{"ida": {Command: "x", Env: map[string]string{"TOKEN": maskedValue}}}
	if err := st.SaveProduct(ProductInput{MCPServers: &servers}); err == nil || !strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("mask without a stored value was accepted: %v", err)
	}
}

// The generated runtime document must carry the same product sections the scan
// needs, so a settings save does not require a second hand-written file.
func TestRuntimeConfigIncludesSavedProductExtensions(t *testing.T) {
	st, _ := productStore(t)
	servers := map[string]MCPServer{"ida": {Command: "idalib-mcp"}}
	if err := st.SaveProduct(ProductInput{MCPServers: &servers, SearchKey: "k1,k2"}); err != nil {
		t.Fatal(err)
	}
	path, err := st.RuntimeConfig(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := runtimeDocument(t, path)
	mcp := runtimeSection(t, doc, "mcp")
	serversDoc := mcp["mcpServers"].(map[string]any)
	if _, ok := serversDoc["ida"]; !ok {
		t.Fatalf("runtime mcpServers missing ida: %s", data)
	}
	search := runtimeSection(t, doc, "search")
	if search["tavily_keys"] != "k1,k2" {
		t.Fatalf("runtime search keys = %v", search["tavily_keys"])
	}
}

func TestConnectConfigPayloadMatchesHarnessShape(t *testing.T) {
	m := Model{Provider: "openai", BaseURL: "http://gateway.test/v1", APIKey: "k", Model: "model-x", ContextWindow: 200000, MaxTokens: 8192}
	config := m.ConnectConfig()
	llm, ok := config["llm"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %v", config)
	}
	providers, ok := llm["providers"].([]any)
	if !ok || len(providers) != 1 {
		t.Fatalf("payload = %v", config)
	}
	entry := providers[0].(map[string]any)
	for key, want := range map[string]any{
		"provider": "openai", "base_url": "http://gateway.test/v1", "api_key": "k", "model": "model-x",
		"context_window": 200000, "max_tokens": 8192,
	} {
		if entry[key] != want {
			t.Fatalf("%s = %v, want %v", key, entry[key], want)
		}
	}
	raw, err := json.Marshal(config)
	if err != nil || !strings.Contains(string(raw), `"llm"`) {
		t.Fatalf("payload does not marshal: %v %s", err, raw)
	}
}

func TestModelsOnlineUsesHostCatalogue(t *testing.T) {
	var request map[string]any
	srv := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/ListModels") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, _ = w.Write([]byte(`{"ok":true,"supported":true,"models":["real-a","real-b"]}`))
	}))
	defer srv.Close()
	st, _ := productStore(t)
	models, ok, err := st.ModelsOnline(context.Background(), srv.URL, "host-token")
	if err != nil || !ok || len(models) != 2 || request["model"] != "route" {
		t.Fatalf("models = %v ok=%v err=%v request=%v", models, ok, err, request)
	}
	unsupported := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"supported":false}`))
	}))
	defer unsupported.Close()
	if _, ok, err := st.ModelsOnline(context.Background(), unsupported.URL, "tok"); err != nil || ok {
		t.Fatalf("unsupported catalogue = ok=%v err=%v", ok, err)
	}
}

func TestCheckModelOnlineUsesHostProbeAndCatalogue(t *testing.T) {
	var sawProbe, sawModels map[string]any
	srv := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer host-token" {
			t.Errorf("Authorization = %q", got)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/TestLLM"):
			_ = json.NewDecoder(r.Body).Decode(&sawProbe)
			_, _ = w.Write([]byte(`{"ok":true,"provider":"openai","model":"served-alias","latencyMs":33}`))
		case strings.HasSuffix(r.URL.Path, "/ListModels"):
			_ = json.NewDecoder(r.Body).Decode(&sawModels)
			_, _ = w.Write([]byte(`{"ok":true,"supported":true,"models":["served-alias","served-alias-2"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	st, _ := productStore(t)
	probe, err := st.CheckModelOnline(context.Background(), srv.URL, "host-token")
	if err != nil {
		t.Fatal(err)
	}
	if !probe.Probed || !probe.Available || probe.ReturnedModel != "served-alias" || probe.Source != "host" {
		t.Fatalf("probe = %+v", probe)
	}
	if probe.MS != 33 {
		t.Fatalf("latency = %d", probe.MS)
	}
	if len(probe.Models) != 2 {
		t.Fatalf("models = %+v", probe.Models)
	}
	if sawProbe["baseUrl"] != "http://gateway.test/v1" || sawProbe["apiKey"] != "gateway-key" || sawProbe["model"] != "route" {
		t.Fatalf("host probe request = %v", sawProbe)
	}
	if sawModels["model"] != "route" {
		t.Fatalf("catalogue request = %v", sawModels)
	}
}

func TestCheckModelOnlineReturnsErrorOnlyOnRPCFailure(t *testing.T) {
	unreachable := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer unreachable.Close()
	st, _ := productStore(t)
	if _, err := st.CheckModelOnline(context.Background(), unreachable.URL, "host-token"); err == nil {
		t.Fatal("a missing ConfigService was reported as a probe answer")
	}

	rejected := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_argument","message":"bad key"}`))
	}))
	defer rejected.Close()
	probe, err := st.CheckModelOnline(context.Background(), rejected.URL, "host-token")
	if err != nil {
		t.Fatalf("a host-side probe rejection is an answer, not a transport error: %v", err)
	}
	if !probe.Probed || probe.Available || !strings.Contains(probe.Error, "bad key") || probe.Source != "host" {
		t.Fatalf("rejected probe = %+v", probe)
	}
}

// A host answer must be usable whatever protojson chose for its numeric fields:
// protojson writes the probe's int64 latency as a string. Failing to decode it
// used to discard the host's verdict, so the console reported a second, direct
// probe instead of the host's own "the key is invalid".
func TestCheckModelOnlineKeepsHostVerdictWithProtoJSONLatency(t *testing.T) {
	srv := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/TestLLM"):
			_, _ = w.Write([]byte(`{"provider":"openai","model":"served-alias","latencyMs":"1608","error":"API error (401): invalid key"}`))
		case strings.HasSuffix(r.URL.Path, "/ListModels"):
			_, _ = w.Write([]byte(`{"supported":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	st, _ := productStore(t)
	probe, err := st.CheckModelOnline(context.Background(), srv.URL, "host-token")
	if err != nil {
		t.Fatalf("a host answer was read as a transport failure: %v", err)
	}
	if !probe.Probed || probe.Source != "host" || probe.Available {
		t.Fatalf("probe = %+v", probe)
	}
	if probe.MS != 1608 {
		t.Fatalf("latency = %d", probe.MS)
	}
	if !strings.Contains(probe.Error, "invalid key") {
		t.Fatalf("host error lost: %q", probe.Error)
	}
}

// An operator file that still uses the removed `extensions:` envelope must keep
// reading: the console shows the stored MCP servers and search keys until the
// next save migrates the file to flat sections.
func TestProductReadsLegacyExtensionsEnvelope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cyber.yaml")
	body := `llm:
    base_url: http://gateway.test/v1
    model: route
    api_key: gateway-key
extensions:
    mcp:
        mcpServers:
            ida:
                command: idalib-mcp
    search:
        tavily_keys: legacy-key
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadAt(filepath.Join(dir, "settings.json"), path)
	if err != nil {
		t.Fatal(err)
	}
	product, err := st.Product()
	if err != nil {
		t.Fatal(err)
	}
	if got := product.MCPServers["ida"]; got.Command != "idalib-mcp" {
		t.Fatalf("legacy mcp = %+v", got)
	}
	if product.SearchKeys != "legacy-key" {
		t.Fatalf("legacy search keys = %q", product.SearchKeys)
	}
}
