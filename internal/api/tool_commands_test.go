package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kksty/saker/internal/executor"
)

func TestShellObservationCountsExecutablesNotQuotedArguments(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{`echo "curl https://example.test"; curl -I example.test && env TOKEN=fixture gogo --help`, []string{"echo", "curl", "gogo"}},
		{`bash -lc 'curl example.test | katana'`, []string{"curl", "katana"}},
		{`'C:/tools/gogo.exe' --help`, []string{"gogo"}},
		{"# curl | katana\ngogo --help # ignored; spray", []string{"gogo"}},
	} {
		if got := shellCommands(tc.command); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: %v != %v", tc.command, got, tc.want)
		}
	}
}

func TestLeaderboardDecodesShellAndKeepsFailureAttributionHonest(t *testing.T) {
	s, _ := newBindingServer(t)
	dir := filepath.Join(s.DataDir, "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data := `{"command":"echo curl; curl example.test | katana"}`
	events := []map[string]any{{"kind": "tool_call", "tool": "bash", "call_id": "c1", "v": executor.JournalSchemaVersion, "raw": map[string]any{"tool_call": map[string]any{"arguments": data}}}, {"kind": "tool_result", "call_id": "c1", "ok": false, "duration_ms": 30, "v": executor.JournalSchemaVersion}}
	var raw []byte
	for _, e := range events {
		b, _ := json.Marshal(e)
		raw = append(raw, append(b, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.jsonl"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	rows, n := s.toolLeaderboard()
	if n != 1 {
		t.Fatal(n)
	}
	found := map[string]ToolStat{}
	for _, row := range rows {
		key := row.Tool
		if row.Local {
			key = "local:" + key
		}
		found[key] = row
	}
	if found["bash"].Failures != 1 || found["bash"].DurationMS != 30 || found["local:curl"].Calls != 1 || found["local:katana"].Calls != 1 {
		t.Fatalf("wrong observations: %+v", found)
	}
	if found["local:curl"].Completed != 0 {
		t.Fatal("pipeline failure incorrectly attributed to each executable")
	}
}

// The producer decodes the protocol's bytes at write time, so a tool_call
// carries its arguments as the original JSON text.
func TestToolCommandReadsPlainArguments(t *testing.T) {
	text := `{"command":"curl example.test | katana"}`
	raw := `{"tool_call":{"arguments":` + string(mustJSON(t, text)) + `}}`
	if got := toolCommand(json.RawMessage(raw)); got != "curl example.test | katana" {
		t.Fatalf("toolCommand(%s) = %q, want the command", raw, got)
	}
	// A JSON string that is not a tool-call body terminates instead of recursing.
	if got := toolCommand(json.RawMessage(`{"arguments":"\"plain\""}`)); got != "" {
		t.Fatalf("plain string arguments = %q, want empty", got)
	}
}
