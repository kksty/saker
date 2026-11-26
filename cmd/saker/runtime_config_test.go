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

func TestRuntimeHarnessArgsExplicitlyUseFilteredConfiguration(t *testing.T) {
	h, source := singleModelHub(t)
	m := h.settings.Effective()
	m.ContextWindow, m.MaxTokens = 32000, 8192
	args, err := runtimeHarnessArgs(h.settings, t.TempDir(), m)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 6 || args[0] != "--config" || !filepath.IsAbs(args[1]) || args[1] == source || strings.Join(args[2:], " ") != "--context-window 32000 --max-tokens 8192" {
		t.Fatalf("runtime arguments=%v", args)
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "paused:") {
		t.Fatal("boot/restart configuration still contains admission metadata")
	}
}

// This exercises the actual strict parser without starting a service or making
// a model request. The filtered runtime document must validate with both the
// rc10 Hub and the scan product.
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
	// rc10 preserves foreign metadata instead of rejecting the source. The
	// materialized file must still remove admission metadata and validate on
	// both products; the unit test above checks the actual filtered bytes.
	args, err := runtimeHarnessArgs(h.settings, dir, h.settings.Effective())
	if err != nil {
		t.Fatal(err)
	}
	for _, binary := range []string{bin, scan} {
		if out, err := validate(binary, args); err != nil {
			t.Fatalf("%s rejected filtered runtime configuration: err=%v output=%s", filepath.Base(binary), err, out)
		}
	}
}
