package aopexec

import (
	"os"
	"path/filepath"
	"testing"

	pb "github.com/kksty/saker/internal/aop/pb"
)

// The Run loop preserves media beside the case and emits one journal event per
// written file, attributed to the turn that carried the message. The event's
// path is relative so a moved project stays readable.
func TestMediaEventsPreservesAndAttributesTheTurn(t *testing.T) {
	workdir := t.TempDir()
	ev := messageEvent("evt-7", textContent("look"), mediaContent("image", "image/png", "shot.png", []byte("png-bytes")))

	events, err := mediaEvents(workdir, ev, "turn-9")
	if err != nil {
		t.Fatalf("mediaEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(events), events)
	}
	got := events[0]
	if got.Kind != MediaKind {
		t.Fatalf("kind = %q, want %q", got.Kind, MediaKind)
	}
	if got.TurnID != "turn-9" || got.SessionID != "session-1" {
		t.Fatalf("attribution = turn %q session %q, want turn-9 session-1", got.TurnID, got.SessionID)
	}
	if want := ".saker/media/evt-7-0.png"; got.Text != want {
		t.Fatalf("text = %q, want %q", got.Text, want)
	}
	if _, err := os.Stat(filepath.Join(workdir, filepath.FromSlash(got.Text))); err != nil {
		t.Fatalf("media not on disk: %v", err)
	}
}

// A rejected medium is reported as an error with the files that were still
// written; the caller turns it into a journal event and does not fail the run.
func TestMediaEventsReportsRejectedMedia(t *testing.T) {
	workdir := t.TempDir()
	oversized := make([]byte, MaxMediaBytes+1)
	ev := messageEvent("evt-8", mediaContent("image", "image/png", "", oversized), mediaContent("image", "image/png", "", []byte("ok")))

	events, err := mediaEvents(workdir, ev, "turn-10")
	if err == nil {
		t.Fatal("expected the over-budget medium to be reported")
	}
	if len(events) != 1 || events[0].Text != ".saker/media/evt-8-1.png" {
		t.Fatalf("events = %+v, want only the second medium", events)
	}
}

// Non-media events must not touch the filesystem or emit anything.
func TestMediaEventsIgnoresOtherPayloads(t *testing.T) {
	ev := &pb.Event{Id: "evt-9", Payload: &pb.Event_Status{Status: &pb.Status{State: "compact_start"}}}
	events, err := mediaEvents(t.TempDir(), ev, "turn-11")
	if err != nil || len(events) != 0 {
		t.Fatalf("mediaEvents(status) = %+v, %v", events, err)
	}
}
