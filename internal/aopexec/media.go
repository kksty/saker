package aopexec

// Media preservation.
//
// A message can carry media (a pasted screenshot, an image the model was given)
// and a tool_result can return media instead of text (browser screenshots,
// audio). contentText only concatenates text, so without a copy the run journal
// can cite a medium by name but nobody can read it back. PreserveMedia writes
// those bytes below the case's own directory — <workdir>/.saker/media — and returns
// path+mime+size so the run can append a journal event (kind=media) next to the
// message/tool_result it belongs to.
//
// The ledger schema is untouched: the journal keeps a path, not the bytes, and
// the evidence admission path still decides what becomes proof. Preserving is
// best effort by contract: one oversized or unwritable item must not abort the
// turn, so every failure is returned as an error (errors.Join) while the other
// media of the same event are still written.
//
// This file deliberately does not modify executor.go (a parallel change owns the
// Run loop); the integration is one call plus emitting Media.Event for each
// returned medium.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/kksty/saker/internal/aop/pb"
	"github.com/kksty/saker/internal/executor"
)

const (
	// MediaDir is the directory that holds preserved media, relative to the
	// case's working directory. It lives under the existing .saker area so project
	// export/import and the workspace sweeps already know it.
	MediaDir = ".saker/media"

	// MaxMediaBytes bounds one preserved medium (8 MiB). It matches the traffic
	// body limit: anything larger is streamed by the tool itself, not embedded
	// in an AOP event, and a huge inline payload would only bloat the case.
	MaxMediaBytes = 8 << 20

	// MaxMediaTotalBytes bounds everything one event preserves (64 MiB), so a
	// single tool result with many images cannot fill the disk in one turn.
	MaxMediaTotalBytes = 64 << 20

	// MediaKind is the journal event kind for a preserved medium. It is a run
	// event, not a fact: the bytes are raw material until an executor cites them.
	MediaKind = "media"
)

// Media is one preserved medium.
//
// Path is relative to the case working directory and always uses forward
// slashes, so a journal row stays portable if the project moves. MIMEType is the
// harness-declared type (may be empty when the source provided none); Bytes is
// the number of bytes written.
type Media struct {
	Path     string `json:"path"`
	MIMEType string `json:"mime,omitempty"`
	Bytes    int    `json:"bytes"`
}

// PreserveMedia writes the media content of a message or tool_result event to
// <workdir>/.saker/media/<event-id>-<idx>.<ext>.
//
// index is the zero-based order of the media content inside the event, so paths
// stay unique and stable when the same event is replayed. A URI-only resource is
// skipped: it is a reference the control plane must not fetch or fabricate. The
// returned slice holds only media that was actually written; error is non-nil if
// any medium was rejected (too large, over budget, unwritable), but the written
// files are still valid and usable.
func PreserveMedia(workdir string, ev *pb.Event) ([]Media, error) {
	return preserveMedia(workdir, ev, MaxMediaBytes, MaxMediaTotalBytes)
}

// preserveMedia is PreserveMedia with injectable limits so the cheap tests can
// exercise the budget arithmetic without allocating 64 MiB fixtures.
func preserveMedia(workdir string, ev *pb.Event, maxItem, maxTotal int) ([]Media, error) {
	if strings.TrimSpace(workdir) == "" {
		return nil, errors.New("保全媒体需要工作目录")
	}
	contents := eventMedia(ev)
	if len(contents) == 0 {
		return nil, nil
	}
	eventID := mediaEventID(ev.GetId())
	var (
		kept  []Media
		errs  []error
		total int
	)
	for index, media := range contents {
		resource := media.GetResource()
		data := resource.GetData()
		if len(data) == 0 {
			// A URI is a pointer, not bytes. The control plane does not fetch
			// remote content on the model's behalf; the reference stays in the
			// journal's text, not in a fake local copy.
			continue
		}
		if len(data) > maxItem {
			errs = append(errs, fmt.Errorf("媒体 #%d 超过单条上限 %d 字节（实际 %d）", index, maxItem, len(data)))
			continue
		}
		if total+len(data) > maxTotal {
			errs = append(errs, fmt.Errorf("媒体 #%d 超过本次保全总量上限 %d 字节（已保全 %d）", index, maxTotal, total))
			continue
		}
		mime := strings.TrimSpace(resource.GetMediaType())
		name := fmt.Sprintf("%s-%d%s", eventID, index, mediaExtension(mime, resource.GetFilename()))
		rel := filepath.Join(MediaDir, name)
		if err := writeMediaFile(filepath.Join(workdir, rel), data); err != nil {
			errs = append(errs, fmt.Errorf("写入媒体 %s：%w", filepath.ToSlash(rel), err))
			continue
		}
		total += len(data)
		kept = append(kept, Media{Path: filepath.ToSlash(rel), MIMEType: mime, Bytes: len(data)})
	}
	return kept, errors.Join(errs...)
}

// Event is the journal event the Run loop appends for one preserved medium,
// attributed to the same session/turn as the event that carried it. The caller
// emits it next to the message/tool_result event; run attribution is added by
// the normal journal path, not invented here.
func (m Media) Event(source *pb.Event) executor.Event {
	raw, _ := json.Marshal(m)
	at := time.Now()
	if ts := source.GetEmittedAt(); ts != nil && ts.IsValid() {
		at = ts.AsTime()
	}
	return executor.Event{
		ID:        source.GetId(),
		TurnID:    source.GetTurnId(),
		SessionID: source.GetSessionId(),
		Kind:      MediaKind,
		At:        at,
		Text:      m.Path,
		Raw:       raw,
	}
}

// eventMedia lists public media content carried by a message or tool_result.
// Other payloads (usage, traffic extensions, …) never carry media here.
func eventMedia(ev *pb.Event) []*pb.MediaContent {
	if ev == nil {
		return nil
	}
	switch payload := ev.GetPayload().(type) {
	case *pb.Event_Message:
		return contentMedia(payload.Message.GetContent())
	case *pb.Event_ToolResult:
		return contentMedia(payload.ToolResult.GetOutput())
	}
	return nil
}

func contentMedia(contents []*pb.Content) []*pb.MediaContent {
	var out []*pb.MediaContent
	for _, content := range contents {
		if content == nil {
			continue
		}
		if media := content.GetMedia(); media != nil {
			out = append(out, media)
		}
		// Some providers nest tool results inside message content; their
		// output can carry media too (for example a browser tool's screenshot).
		if result := content.GetToolResult(); result != nil {
			out = append(out, contentMedia(result.GetOutput())...)
		}
	}
	return out
}

// mediaEventID turns the harness event id into a safe single path element. The
// id is opaque protocol data; sanitizing keeps "../" or Windows reserved
// separators from escaping the media directory.
func mediaEventID(id string) string {
	id = strings.TrimSpace(id)
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "event"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// mediaExtensions is the deterministic extension table. Not mime.ExtensionsByType:
// that reads the Windows registry, so the same event would land at a different
// path on two machines.
var mediaExtensions = map[string]string{
	"image/png":        ".png",
	"image/jpeg":       ".jpg",
	"image/gif":        ".gif",
	"image/webp":       ".webp",
	"image/bmp":        ".bmp",
	"image/tiff":       ".tiff",
	"image/svg+xml":    ".svg",
	"audio/mpeg":       ".mp3",
	"audio/wav":        ".wav",
	"audio/x-wav":      ".wav",
	"audio/ogg":        ".ogg",
	"audio/mp4":        ".m4a",
	"video/mp4":        ".mp4",
	"video/webm":       ".webm",
	"application/pdf":  ".pdf",
	"application/json": ".json",
	"text/plain":       ".txt",
	"text/html":        ".html",
}

func mediaExtension(mimeType, filename string) string {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
	if ext, ok := mediaExtensions[base]; ok {
		return ext
	}
	if ext := safeExtension(filepath.Ext(filename)); ext != "" {
		return ext
	}
	if slash := strings.IndexByte(base, '/'); slash >= 0 {
		if ext := safeExtension("." + base[slash+1:]); ext != "" {
			return ext
		}
	}
	return ".bin"
}

func safeExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if len(ext) < 2 || len(ext) > 11 {
		return ""
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}

// writeMediaFile stages beside the target and renames over it: a reader (the
// console or a later run) must never see half a preserved medium, even if the
// control plane dies mid-write.
func writeMediaFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".media-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
