package api

// Real-harness integration coverage for P0-2: the console's tool roots must be
// the directories the running scan actually resolves, and tool/model
// availability must come from the live AOP catalogue — not from a file that
// happens to exist. Opt in with SAKER_HARNESS_BIN (+ SAKER_SCAN_BIN); without the
// pair the test skips, so the default `go test ./...` stays hermetic.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kksty/saker/internal/testutil"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kksty/saker/internal/aop"
	"github.com/kksty/saker/internal/host"
	"github.com/kksty/saker/internal/node"
	"github.com/kksty/saker/internal/skills"
	"github.com/kksty/saker/internal/store"
)

// sameDirAsRuntime mirrors host.sameWorkingDir: Windows compares
// case-insensitively, every other platform compares cleaned paths.
func sameDirAsRuntime(a, b string) bool {
	a, b = filepath.Clean(strings.TrimSpace(a)), filepath.Clean(strings.TrimSpace(b))
	if a == "" || b == "" || a == "." || b == "." {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func TestHarnessToolRootsMatchRuntimeCatalogue(t *testing.T) {
	bin := os.Getenv("SAKER_HARNESS_BIN")
	if bin == "" {
		bin = os.Getenv("SAKER_AISCAN_BIN")
	}
	scan := os.Getenv("SAKER_SCAN_BIN")
	if bin == "" || scan == "" {
		t.Skip("set SAKER_HARNESS_BIN and SAKER_SCAN_BIN to check the real harness tool roots")
	}

	// A local fixture model: the hub must bind a provider/model for readiness.
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer gateway.Close()

	// saker's production layout: data dir holds projects/<id>/; the supervised
	// scan is started with its working directory at the data dir
	// (internal/host/host.go execLaunch). The commander skill creates
	// <data>/.cyber/skills, which is exactly the directory that makes the
	// harness resolve its data dir to <data>/.cyber.
	dataDir := t.TempDir()
	if _, err := skills.Install(skills.Commander, filepath.Join(dataDir, ".cyber", "skills")); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(dataDir, "projects", "default")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dataDir, "fixture.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("llm:\n  provider: openai\n  base_url: %s/v1\n  api_key: fixture-key\n  model: fixture-model\n", gateway.URL)), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	h, err := host.Start(ctx, host.Config{
		Bin: bin, NodeBin: scan, Dir: dataDir,
		Args:            []string{"--config", config},
		DropEnvPrefixes: []string{"CYBER_", "SAKER_CONSOLE_TOKEN", "SAKER_AGENT_TOKEN"},
		RequireProvider: true, ReadyTimeout: 45 * time.Second, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	// The scan's own report is the ground truth for where it reads tools from.
	agents, err := aop.New(h.Endpoint(), h.Token()).Agents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	live, ok := aop.Find(agents, h.Agent())
	if !ok {
		t.Fatalf("runtime did not report the supervised scan: %+v", agents)
	}
	if !sameDirAsRuntime(live.WorkingDir, dataDir) {
		t.Fatalf("scan working dir = %q, want the instance data dir %q", live.WorkingDir, dataDir)
	}

	st, err := store.Open(filepath.Join(projectDir, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	s := New(st, nil, projectDir, filepath.Join(projectDir, "cases"))
	registry, err := node.New([]node.Spec{{ID: "local", AOP: h.Endpoint(), Token: h.Token(), Agent: h.Agent()}}, "local")
	if err != nil {
		t.Fatal(err)
	}
	s.Exec = registry.Executor(0)
	s.LocalNode, s.LocalHostStatus = "local", h.Status

	// A tool that exists only in the harness-resolved arsenal directory. It is
	// written after readiness so the host's own arsenal preparation cannot
	// overwrite it; the console must attribute it to the harness root.
	harnessBin := filepath.Join(dataDir, ".cyber", "arsenal", "bin")
	if err := os.MkdirAll(harnessBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(harnessBin, "p02-runtime-fixture"), []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	s.Register(mux)
	server := testutil.NewServer(t, mux)
	defer server.Close()
	get := func(path string) []byte {
		t.Helper()
		res, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, res.StatusCode)
		}
		var body []byte
		body, err = io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}

	var tools toolsResponse
	body := get("/api/tools")
	if err := json.Unmarshal(body, &tools); err != nil {
		t.Fatalf("tools response is not JSON: %v\n%s", err, body)
	}

	// 1) Runtime availability is the live host's declaration, with model/health.
	if tools.Runtime == nil {
		t.Fatal("tools response has no runtime section")
	}
	if tools.Runtime.State != "connected" || tools.Runtime.Model != "fixture-model" || tools.Runtime.AgentID != h.Agent() {
		t.Fatalf("runtime section = %+v, want connected fixture-model agent %s", tools.Runtime, h.Agent())
	}
	if len(tools.Runtime.Commands) == 0 {
		t.Fatal("live harness declared no commands; the executor would see an empty capability set")
	}
	declared := map[string]bool{}
	for _, command := range tools.Runtime.Commands {
		declared[command.Name] = true
	}
	if !declared["!arsenal"] {
		t.Fatalf("live harness did not declare !arsenal: %+v", tools.Runtime.Commands)
	}

	// The dedicated endpoint is the same implementation and keeps its contract.
	var runtimeTools runtimeToolsResponse
	body = get("/api/tools/runtime")
	if err := json.Unmarshal(body, &runtimeTools); err != nil {
		t.Fatalf("runtime response is not JSON: %v", err)
	}
	if runtimeTools.State != "connected" || len(runtimeTools.Commands) == 0 || runtimeTools.Model != tools.Runtime.Model {
		t.Fatalf("runtime endpoint disagrees with tools.runtime: %+v vs %+v", runtimeTools, tools.Runtime)
	}

	// 2) The console reports the directory the scan actually resolves, derived
	//    from the scan's own working directory, and labels every candidate.
	if tools.ToolRootsNote == "" {
		t.Error("tool_roots_note is empty: the roots would look like guesses")
	}
	harnessRoot := findToolRoot(t, tools.ToolRoots, "harness")
	wantHarness := filepath.Join(live.WorkingDir, ".cyber", "arsenal")
	if !sameDirAsRuntime(harnessRoot.Path, wantHarness) {
		t.Fatalf("harness tool root = %q, want %q (scan working dir %q)", harnessRoot.Path, wantHarness, live.WorkingDir)
	}
	if !harnessRoot.Resolved || !harnessRoot.PathInjected || !sameDirAsRuntime(harnessRoot.Bin, harnessBin) {
		t.Fatalf("harness tool root is not the live PATH/precedence root: %+v", harnessRoot)
	}
	// The user fallback is listed but not resolved while <data>/.cyber exists.
	userRoot := findToolRoot(t, tools.ToolRoots, "user")
	if userRoot.Resolved {
		t.Fatalf("user root still claims to be resolved: %+v", userRoot)
	}
	if legacy := findToolRoot(t, tools.ToolRoots, "instance"); legacy.Resolved {
		t.Fatalf("legacy instance root claims to be resolved: %+v", legacy)
	}

	// 3) Inventory follows the same provenance: the fixture is reported from the
	//    harness bin, and the page names the source next to the path.
	var fixture *ArsenalTool
	for i := range tools.Arsenal {
		if tools.Arsenal[i].Name == "p02-runtime-fixture" {
			fixture = &tools.Arsenal[i]
		}
	}
	if fixture == nil {
		t.Fatalf("console did not see the tool installed in the harness root %q: %+v", harnessBin, tools.Arsenal)
	}
	if len(fixture.Dirs) != 1 || !sameDirAsRuntime(fixture.Dirs[0], harnessBin) {
		t.Fatalf("fixture dirs = %v, want harness root bin %q", fixture.Dirs, harnessBin)
	}
}
