package api

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/kksty/saker/internal/host"
)

// Peak memory of the local host's child processes, written once per settled run.
//
// The incident behind this: a machine black-screened with the system commit
// charge at 99.8% after a PoC interpreter reached 28.7 GiB, and nothing in the
// ledger could name which process it was. One record per run answers "which
// one, in which round" after the fact.
//
// This is bookkeeping only. Nothing here caps memory, kills a process or feeds
// scheduling; a value above host.PeakRSSWarnBytes adds one warning line and
// nothing else.

// procPeakEntry is one sampled process in the journal record.
type procPeakEntry struct {
	PID       int    `json:"pid"`
	Name      string `json:"name"`
	PeakBytes uint64 `json:"peak_rss_bytes"`
	// Available false means the peak could not be read (the process already
	// exited, the host was down, the platform refused). It is recorded as such
	// instead of as 0 bytes, which would read as "it stayed tiny".
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type procPeakEvent struct {
	Kind      string          `json:"kind"`
	At        time.Time       `json:"at"`
	Processes []procPeakEntry `json:"processes"`
	V         int             `json:"v,omitempty"` // stamped by runJournal.append
}

// recordChildPeaks samples the host's child processes into the run journal.
// Both callers run it immediately before flushRunWarnings, so the >4 GiB warning
// it may add reaches the event stream through the existing single writer.
func (s *Server) recordChildPeaks(runID string) {
	if s == nil || s.ChildPeaks == nil || strings.TrimSpace(runID) == "" {
		return
	}
	peaks := s.ChildPeaks()
	if len(peaks) == 0 {
		// An empty sample is "this instance supervises no process", not "nothing
		// was large": a record of zero processes would be noise in every run.
		return
	}
	ev := procPeakEvent{Kind: "proc_peak", At: time.Now(), Processes: make([]procPeakEntry, 0, len(peaks))}
	for _, p := range peaks {
		ev.Processes = append(ev.Processes, procPeakEntry{
			PID: p.PID, Name: p.Name, PeakBytes: p.Bytes, Available: p.Available, Reason: p.Reason,
		})
	}
	if err := s.appendEvent(runID, ev); err != nil && !errors.Is(err, errNoJournal) {
		log.Printf("run %s：记录子进程峰值内存失败：%v", runID, err)
	}
	for _, p := range peaks {
		if !p.Available || p.Bytes <= host.PeakRSSWarnBytes {
			continue
		}
		s.addRunWarning(runID, fmt.Sprintf(
			"子进程峰值内存 %.1f GiB 超过 %d GiB 记账阈值：pid=%d name=%s（只记录，未设上限、未终止进程）",
			float64(p.Bytes)/(1<<30), host.PeakRSSWarnBytes>>30, p.PID, peakProcessName(p.Name)))
	}
}

func peakProcessName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "未知"
	}
	return name
}
