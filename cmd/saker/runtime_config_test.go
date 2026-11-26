package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Hub and the scan read two different runtime documents: the scan declares
// the product sections and reads the full file; the Hub's strict validator
// rejects every section it does not declare, so it reads the shared-only
// projection. Both are generated from the operator's document.
func TestRuntimeHarnessArgsSplitHubAndScanDocuments(t *testing.T) {
	h, source := singleModelHub(t)
	// One product section makes the split observable in the bytes.
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw) + "mcp:\n    mcpServers:\n        ida:\n            command: ida-mcp\n"
	if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m := h.settings.Effective()
	m.ContextWindow, m.MaxTokens = 32000, 8192
	hubArgs, scanArgs, err := runtimeHarnessArgs(h.settings, t.TempDir(), m)
	if err != nil {
		t.Fatal(err)
	}
	configPath := func(name string, args []string) string {
		t.Helper()
		if len(args) != 6 || args[0] != "--config" || !filepath.IsAbs(args[1]) || args[1] == source || strings.Join(args[2:], " ") != "--context-window 32000 --max-tokens 8192" {
			t.Fatalf("%s arguments=%v", name, args)
		}
		return args[1]
	}
	hubPath := configPath("hub", hubArgs)
	scanPath := configPath("scan", scanArgs)
	if hubPath == scanPath {
		t.Fatal("hub and scan share one document; the hub would reject the product sections")
	}
	scanData, err := os.ReadFile(scanPath)
	if err != nil {
		t.Fatal(err)
	}
	hubData, err := os.ReadFile(hubPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(scanData), "mcpServers") || !strings.Contains(string(scanData), "fast-model") {
		t.Fatalf("scan document lost the product or shared sections:\n%s", scanData)
	}
	if strings.Contains(string(hubData), "mcpServers") || strings.Contains(string(hubData), "extensions:") {
		t.Fatalf("hub document still carries scan-only sections:\n%s", hubData)
	}
	if !strings.Contains(string(hubData), "fast-model") {
		t.Fatalf("hub document lost the model binding:\n%s", hubData)
	}
	if strings.Contains(string(scanData), "paused:") || strings.Contains(string(hubData), "paused:") {
		t.Fatal("boot/restart configuration still contains admission metadata")
	}
}

// A legacy `extensions:` envelope is promoted to flat root sections on the way
// into the generated runtime documents; the harness removed the envelope.
func TestRuntimeHarnessArgsFlattenLegacyExtensions(t *testing.T) {
	h, source := singleModelHub(t)
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `extensions:
    guardrail:
        provider: llm
        on_error: review
        llm:
            base_url: http://judge.test/v1
            model: judge-small
    mcp:
        mcpServers:
            ida:
                command: ida-mcp
`
	if err := os.WriteFile(source, append(raw, []byte(legacy)...), 0o600); err != nil {
		t.Fatal(err)
	}
	_, scanArgs, err := runtimeHarnessArgs(h.settings, t.TempDir(), h.settings.Effective())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(scanArgs[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "extensions:") {
		t.Fatalf("generated runtime document still carries the legacy envelope:\n%s", data)
	}
	for _, section := range []string{"guardrail:", "mcp:"} {
		if !strings.Contains(string(data), section) {
			t.Fatalf("flattened runtime document lost %s:\n%s", section, data)
		}
	}
}

// This exercises the actual strict parser without starting a service or making
// a model request: the Hub reads its projection and the scan reads the full
// document, and each product must accept the file Saker generates for it.
func TestRuntimeHarnessConfigStrictParser(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("SAKER_HARNESS_BIN"))
	if bin == "" {
		bin = strings.TrimSpace(os.Getenv("SAKER_AISCAN_BIN"))
	}
	if bin == "" {
		t.Skip("set SAKER_HARNESS_BIN to validate with the real harness parser")
	}
	h, _ := singleModelHub(t)
	dir := t.TempDir()
	validate := func(binary string, args []string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append(args, "config", "validate")...)
		cmd.Dir = dir
		return cmd.CombinedOutput()
	}
	scan := os.Getenv("SAKER_SCAN_BIN")
	if scan == "" {
		scan = filepath.Join(filepath.Dir(bin), "cyber-scan"+filepath.Ext(bin))
	}
	if st, err := os.Stat(scan); err != nil || st.IsDir() {
		t.Skip("set SAKER_SCAN_BIN (or put cyber-scan next to SAKER_HARNESS_BIN) for the strict parser check")
	}
	hubArgs, scanArgs, err := runtimeHarnessArgs(h.settings, dir, h.settings.Effective())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := validate(bin, hubArgs); err != nil {
		t.Fatalf("%s rejected the hub runtime configuration: err=%v output=%s", filepath.Base(bin), err, out)
	}
	if out, err := validate(scan, scanArgs); err != nil {
		t.Fatalf("%s rejected the scan runtime configuration: err=%v output=%s", filepath.Base(scan), err, out)
	}
}
