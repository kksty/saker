package aopexec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/kksty/saker/internal/aop/pb"
	"github.com/kksty/saker/internal/executor"
)

func mediaContent(kind, mimeType, filename string, data []byte) *pb.Content {
	return &pb.Content{Value: &pb.Content_Media{Media: &pb.MediaContent{
		Kind: kind,
		Resource: &pb.Resource{
			Source:    &pb.Resource_Data{Data: data},
			MediaType: mimeType,
			Filename:  filename,
		},
	}}}
}

func textContent(text string) *pb.Content {
	return &pb.Content{Value: &pb.Content_Text{Text: &pb.TextContent{Text: text}}}
}

func messageEvent(id string, contents ...*pb.Content) *pb.Event {
	return &pb.Event{Id: id, SessionId: "session-1", TurnId: "turn-1",
		Payload: &pb.Event_Message{Message: &pb.Message{Role: "assistant", Content: contents}}}
}

func toolResultEvent(id string, contents ...*pb.Content) *pb.Event {
	return &pb.Event{Id: id, SessionId: "session-1", TurnId: "turn-1",
		Payload: &pb.Event_ToolResult{ToolResult: &pb.ToolResult{Name: "browser", Output: contents}}}
}

// A message that carries an image must land on disk next to the case, with the
// mime type and byte count the journal needs. The index makes the path stable
// across replays, and the text sibling is ignored.
func TestPreserveMediaWritesMessageImage(t *testing.T) {
	workdir := t.TempDir()
	image := []byte("png-bytes")
	ev := messageEvent("evt-1", textContent("look at this"), mediaContent("image", "image/png", "shot.png", image))

	medias, err := PreserveMedia(workdir, ev)
	if err != nil {
		t.Fatalf("PreserveMedia: %v", err)
	}
	if len(medias) != 1 {
		t.Fatalf("preserved %d media, want 1: %+v", len(medias), medias)
	}
	want := ".saker/media/evt-1-0.png"
	if medias[0].Path != want {
		t.Fatalf("path = %q, want %q", medias[0].Path, want)
	}
	if medias[0].MIMEType != "image/png" {
		t.Fatalf("mime = %q, want image/png", medias[0].MIMEType)
	}
	if medias[0].Bytes != len(image) {
		t.Fatalf("bytes = %d, want %d", medias[0].Bytes, len(image))
	}
	data, err := os.ReadFile(filepath.Join(workdir, filepath.FromSlash(medias[0].Path)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(image) {
		t.Fatalf("preserved bytes = %q, want %q", data, image)
	}
}

// tool_result media follows the same rule, including extensions inferred from a
// filename when the resource has no mime type, and a tool result nested inside
// message content (Anthropic-style blocks) is not missed.
func TestPreserveMediaWritesToolResultAndNestedMedia(t *testing.T) {
	workdir := t.TempDir()
	audio := []byte("wav-bytes")
	ev := toolResultEvent("evt-2", mediaContent("audio", "audio/wav", "", audio))

	medias, err := PreserveMedia(workdir, ev)
	if err != nil {
		t.Fatalf("PreserveMedia: %v", err)
	}
	if len(medias) != 1 || medias[0].Path != ".saker/media/evt-2-0.wav" {
		t.Fatalf("tool_result media = %+v, want .saker/media/evt-2-0.wav", medias)
	}

	nested := messageEvent("evt-3", &pb.Content{Value: &pb.Content_ToolResult{ToolResult: &pb.ToolResult{
		Name:   "browser",
		Output: []*pb.Content{mediaContent("image", "", "capture.bin", []byte("raw"))},
	}}})
	medias, err = PreserveMedia(workdir, nested)
	if err != nil {
		t.Fatalf("nested PreserveMedia: %v", err)
	}
	if len(medias) != 1 || medias[0].Path != ".saker/media/evt-3-0.bin" {
		t.Fatalf("nested tool_result media = %+v, want .saker/media/evt-3-0.bin", medias)
	}
	if data, err := os.ReadFile(filepath.Join(workdir, ".saker", "media", "evt-3-0.bin")); err != nil || string(data) != "raw" {
		t.Fatalf("nested media bytes = %q, %v", data, err)
	}
}

// A URI-only resource is a pointer, not something the control plane may fetch:
// it is skipped without writing a fake local copy and without failing the turn.
func TestPreserveMediaSkipsURIOnlyResource(t *testing.T) {
	workdir := t.TempDir()
	ev := messageEvent("evt-4", &pb.Content{Value: &pb.Content_Media{Media: &pb.MediaContent{
		Kind:     "image",
		Resource: &pb.Resource{Source: &pb.Resource_Uri{Uri: "https://example.test/shot.png"}, MediaType: "image/png"},
	}}})
	medias, err := PreserveMedia(workdir, ev)
	if err != nil {
		t.Fatalf("URI-only resource returned an error: %v", err)
	}
	if len(medias) != 0 {
		t.Fatalf("URI-only resource was written: %+v", medias)
	}
	if _, err := os.Stat(filepath.Join(workdir, ".saker", "media")); !os.IsNotExist(err) {
		t.Fatalf("media dir created without bytes: %v", err)
	}
}

func TestPreserveMediaIgnoresEventsWithoutMedia(t *testing.T) {
	medias, err := PreserveMedia(t.TempDir(), messageEvent("evt-5", textContent("plain")))
	if err != nil || len(medias) != 0 {
		t.Fatalf("text-only event = %+v, %v", medias, err)
	}
	if medias, err := PreserveMedia(t.TempDir(), &pb.Event{Id: "evt-6"}); err != nil || len(medias) != 0 {
		t.Fatalf("payload-less event = %+v, %v", medias, err)
	}
}

// Limits are best effort: an oversized item is refused with a named error, a
// smaller sibling in the same event is still preserved, and the total budget
// stops later items without panicking.
func TestPreserveMediaEnforcesItemAndTotalLimits(t *testing.T) {
	workdir := t.TempDir()
	ev := messageEvent("evt-7",
		mediaContent("image", "image/png", "", []byte("abc")),   // 3 bytes: fits
		mediaContent("image", "image/png", "", []byte("12345")), // 5 bytes: over the item cap
		mediaContent("image", "image/png", "", []byte("def")),   // 3 bytes: total 6
		mediaContent("image", "image/png", "", []byte("g")),     // 1 byte: over the total budget
	)
	medias, err := preserveMedia(workdir, ev, 4, 6)
	if err == nil {
		t.Fatal("oversized media did not report an error")
	}
	if !strings.Contains(err.Error(), "单条上限") || !strings.Contains(err.Error(), "总量上限") {
		t.Fatalf("error does not name both limits: %v", err)
	}
	if len(medias) != 2 {
		t.Fatalf("preserved %+v, want two in-budget items", medias)
	}
	for _, media := range medias {
		if _, err := os.Stat(filepath.Join(workdir, filepath.FromSlash(media.Path))); err != nil {
			t.Fatalf("kept media %s missing: %v", media.Path, err)
		}
	}

	// The exported default cap rejects a body just over 8 MiB without writing it.
	huge := messageEvent("evt-8", mediaContent("image", "image/png", "", make([]byte, MaxMediaBytes+1)))
	if _, err := PreserveMedia(workdir, huge); err == nil {
		t.Fatal("over MaxMediaBytes media was accepted")
	}
	if _, err := os.Stat(filepath.Join(workdir, ".saker", "media", "evt-8-0.png")); !os.IsNotExist(err) {
		t.Fatalf("oversized media was written: %v", err)
	}
}

// A bad workdir must return an error, not panic or half-write outside the case:
// here .saker is a regular file, so the media directory cannot be created.
func TestPreserveMediaReportsUnwritableWorkdir(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, ".saker"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	ev := messageEvent("evt-9", mediaContent("image", "image/png", "", []byte("x")))
	medias, err := PreserveMedia(workdir, ev)
	if err == nil {
		t.Fatal("unwritable workdir did not report an error")
	}
	if len(medias) != 0 {
		t.Fatalf("unwritable workdir returned media: %+v", medias)
	}
}

// The event id is protocol data: path separators and "." segments must not let a
// medium escape <workdir>/.saker/media.
func TestPreserveMediaSanitizesEventID(t *testing.T) {
	workdir := t.TempDir()
	ev := messageEvent(`../../evil\id`, mediaContent("image", "image/png", "", []byte("x")))
	medias, err := PreserveMedia(workdir, ev)
	if err != nil {
		t.Fatalf("PreserveMedia: %v", err)
	}
	if len(medias) != 1 {
		t.Fatalf("preserved %+v", medias)
	}
	cleaned := filepath.Clean(filepath.FromSlash(medias[0].Path))
	if filepath.Dir(cleaned) != filepath.FromSlash(MediaDir) {
		t.Fatalf("event id escaped the media directory: %q", medias[0].Path)
	}
	rel, err := filepath.Rel(workdir, filepath.Join(workdir, cleaned))
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("media path is not below the workdir: %q (%v)", rel, err)
	}
	if _, err := os.Stat(filepath.Join(workdir, cleaned)); err != nil {
		t.Fatalf("sanitized media path missing: %v", err)
	}
}

// The journal event is what the Run loop emits: kind=media, the same event id,
// session and turn, the relative path as text, and mime/bytes in raw JSON.
func TestMediaEventCarriesJournalIdentity(t *testing.T) {
	workdir := t.TempDir()
	ev := messageEvent("evt-10", mediaContent("image", "image/png", "a.png", []byte("png")))
	medias, err := PreserveMedia(workdir, ev)
	if err != nil || len(medias) != 1 {
		t.Fatalf("PreserveMedia = %+v, %v", medias, err)
	}
	journal := medias[0].Event(ev)
	if journal.Kind != MediaKind {
		t.Fatalf("kind = %q, want %q", journal.Kind, MediaKind)
	}
	if journal.ID != "evt-10" || journal.SessionID != "session-1" || journal.TurnID != "turn-1" {
		t.Fatalf("journal identity = id %q session %q turn %q", journal.ID, journal.SessionID, journal.TurnID)
	}
	if journal.Text != medias[0].Path {
		t.Fatalf("journal text = %q, want path %q", journal.Text, medias[0].Path)
	}
	var raw Media
	if err := json.Unmarshal(journal.Raw, &raw); err != nil {
		t.Fatalf("journal raw is not a Media: %v", err)
	}
	if raw.Path != medias[0].Path || raw.MIMEType != "image/png" || raw.Bytes != 3 {
		t.Fatalf("journal raw = %+v, want %+v", raw, medias[0])
	}
}

func TestMediaExtensionIsDeterministic(t *testing.T) {
	cases := []struct{ mime, filename, want string }{
		{"image/png", "", ".png"},
		{"image/jpeg; charset=binary", "", ".jpg"},
		{"IMAGE/WEBP", "ignored.gif", ".webp"},
		{"", "shot.PNG", ".png"},
		{"", "noext", ".bin"},
		{"image/x-custom", "", ".bin"},
		{"application/octet-stream", "archive.7z", ".7z"},
	}
	for _, c := range cases {
		if got := mediaExtension(c.mime, c.filename); got != c.want {
			t.Fatalf("mediaExtension(%q, %q) = %q, want %q", c.mime, c.filename, got, c.want)
		}
	}
}

// executor.Event is the journal's shape; keep the compiler honest about the
// constants this file returns (a typo here would be silently untyped).
var _ executor.Event = Media{}.Event(&pb.Event{})
