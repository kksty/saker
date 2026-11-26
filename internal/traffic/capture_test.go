package traffic

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	native "github.com/kksty/saker/internal/aop/traffic"
	"google.golang.org/protobuf/proto"
)

func captureFixture() *native.Flow {
	return &native.Flow{Id: "flow-1", Complete: true, Request: &native.HttpRequest{Method: "GET", Url: "https://example.test/first"}, Response: &native.HttpResponse{StatusCode: 200, Body: []byte("original")}}
}

func TestMissingCommittedFlowBodyDoesNotAuthorizeReplacingItsObservation(t *testing.T) {
	dir := t.TempDir()
	flow := captureFixture()
	r, err := SaveCapture(dir, "run-1", "intent-1", "session-1", "event-1", flow, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := captureBase(dir, r.Ref)
	before, _ := os.ReadFile(path + ".json")
	if err := os.Remove(path + ".pb"); err != nil {
		t.Fatal(err)
	}
	flow.Request.Url = "https://example.test/different"
	if _, err := SaveCapture(dir, "run-1", "intent-1", "session-1", "event-1", flow, nil); err == nil {
		t.Fatal("missing body silently replaced a committed observation")
	}
	after, _ := os.ReadFile(path + ".json")
	if string(before) != string(after) {
		t.Fatal("committed metadata changed")
	}
}

func TestReadingAFlowRejectsAnOriginalWithAnotherIdentity(t *testing.T) {
	dir := t.TempDir()
	flow := captureFixture()
	r, err := SaveCapture(dir, "run-1", "intent-1", "session-1", "event-1", flow, nil)
	if err != nil {
		t.Fatal(err)
	}
	flow.Id = "another-flow"
	b, _ := proto.Marshal(flow)
	if err := os.WriteFile(captureBase(dir, r.Ref)+".pb", b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCapture(dir, r.Ref); err == nil {
		t.Fatal("wrong original displayed under recorded identity")
	}
}

func TestReadingAFlowRejectsSameLengthBodyDamage(t *testing.T) {
	dir := t.TempDir()
	flow := captureFixture()
	r, err := SaveCapture(dir, "run-1", "intent-1", "session-1", "event-1", flow, nil)
	if err != nil {
		t.Fatal(err)
	}
	flow.Response.Body = []byte("tampered")
	b, _ := proto.Marshal(flow)
	if err := os.WriteFile(captureBase(dir, r.Ref)+".pb", b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCapture(dir, r.Ref); err == nil {
		t.Fatal("changed body was served as the original observation")
	}
}

func TestReadingLegacyFlowStillChecksOriginalIdentity(t *testing.T) {
	dir := t.TempDir()
	flow := captureFixture()
	r, err := SaveCapture(dir, "run-1", "intent-1", "session-1", "event-1", flow, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.PayloadSHA256 = ""
	b, _ := json.Marshal(r)
	if err := os.WriteFile(captureBase(dir, r.Ref)+".json", b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCapture(dir, r.Ref); err != nil {
		t.Fatal("legacy observation became unreadable", err)
	}
	flow.Id = "other"
	b, _ = proto.Marshal(flow)
	if err := os.WriteFile(captureBase(dir, r.Ref)+".pb", b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCapture(dir, r.Ref); err == nil {
		t.Fatal("legacy mismatched body accepted")
	}
}

// The harness records "body truncated (x/y bytes retained)" when it kept fewer
// bytes than observed, and "(preview only; …)" when the body was never written
// to disk. The capture index answers those questions without making the console
// parse harness prose itself.
func TestCaptureDerivesPreviewAndTruncationFlags(t *testing.T) {
	cases := []struct {
		name      string
		flowError string
		preview   bool
		truncated bool
	}{
		{"no error", "", false, false},
		{"truncated only", "response body truncated (100/200 bytes retained)", false, true},
		{"preview only", "request body truncated (0/200 bytes retained) (preview only; local storage disabled or unavailable)", true, true},
		{"storage only", "body storage: traffic: body storage disabled", false, false},
	}
	for index, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			flow := captureFixture()
			flow.Error = c.flowError
			runID := fmt.Sprintf("run-%d", index)
			r, err := SaveCapture(dir, runID, "intent-1", "session-1", "event-1", flow, nil)
			if err != nil {
				t.Fatal(err)
			}
			if r.PreviewOnly != c.preview || r.Truncated != c.truncated {
				t.Fatalf("saved flags = preview %v truncated %v, want %v/%v", r.PreviewOnly, r.Truncated, c.preview, c.truncated)
			}
			read, _, err := ReadCapture(dir, r.Ref)
			if err != nil {
				t.Fatal(err)
			}
			if read.PreviewOnly != c.preview || read.Truncated != c.truncated {
				t.Fatalf("read flags = preview %v truncated %v, want %v/%v", read.PreviewOnly, read.Truncated, c.preview, c.truncated)
			}
			rows, total, err := ListCaptures(dir, "", "", 0, 10)
			if err != nil || total != 1 || len(rows) != 1 {
				t.Fatalf("ListCaptures = %+v, %d, %v", rows, total, err)
			}
			if rows[0].PreviewOnly != c.preview || rows[0].Truncated != c.truncated {
				t.Fatalf("listed flags = preview %v truncated %v, want %v/%v", rows[0].PreviewOnly, rows[0].Truncated, c.preview, c.truncated)
			}
			raw, err := os.ReadFile(captureBase(dir, r.Ref) + ".json")
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{`"preview_only":`, `"truncated":`} {
				if !strings.Contains(string(raw), key) {
					t.Fatalf("saved index lacks %s: %s", key, raw)
				}
			}
		})
	}
}

// Index rows written before the fields existed have no boolean keys. They must
// stay readable (false defaults), and the preserved Error text — when it carries
// the harness markers — reconciles the flags instead of leaving them wrong.
func TestLegacyCaptureJSONWithoutFlags(t *testing.T) {
	dir := t.TempDir()
	plain, err := SaveCapture(dir, "run-plain", "intent-1", "session-1", "event-1", captureFixture(), nil)
	if err != nil {
		t.Fatal(err)
	}
	truncatedFlow := captureFixture()
	truncatedFlow.Id = "flow-2"
	truncatedFlow.Error = "response body truncated (10/20 bytes retained) (preview only; local storage disabled or unavailable)"
	marked, err := SaveCapture(dir, "run-marked", "intent-1", "session-1", "event-2", truncatedFlow, nil)
	if err != nil {
		t.Fatal(err)
	}
	stripNewFields := func(c Capture) {
		path := captureBase(dir, c.Ref) + ".json"
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		delete(document, "preview_only")
		delete(document, "truncated")
		raw, err = json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	stripNewFields(plain)
	stripNewFields(marked)

	readPlain, _, err := ReadCapture(dir, plain.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if readPlain.PreviewOnly || readPlain.Truncated {
		t.Fatalf("legacy clean row gained flags: %+v", readPlain)
	}
	readMarked, _, err := ReadCapture(dir, marked.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !readMarked.PreviewOnly || !readMarked.Truncated {
		t.Fatalf("legacy row with a preview error = preview %v truncated %v, want true/true", readMarked.PreviewOnly, readMarked.Truncated)
	}
	rows, total, err := ListCaptures(dir, "", "", 0, 10)
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("ListCaptures = %+v, %d, %v", rows, total, err)
	}
	for _, row := range rows {
		want := !strings.Contains(row.Error, "preview only")
		if row.PreviewOnly == want {
			t.Fatalf("legacy listed row %q has preview_only=%v (error %q)", row.Ref, row.PreviewOnly, row.Error)
		}
	}
}
