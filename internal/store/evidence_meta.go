package store

import (
	"fmt"
	"strings"

	"github.com/kksty/saker/internal/evidence"
	"github.com/kksty/saker/internal/model"
)

// normalizeEvidence keeps a row's preservation metadata structurally coherent
// before it can be written. It is not a read-time check (evidence.Read does
// that): it makes the impossible states impossible to record in the first place.
//
// A URI inside the content-addressed store is only meaningful together with the
// digest encoded in its name; accepting such a row without a digest would create
// an "immutable" row no reader can verify. A row with no digest at all is
// recorded as given: every production writer (ledger ingest/import, traffic
// capture) records the digest evidence.Preserve returns, so such a row can only
// come from a hand-edited or imported ledger — and no reader serves it, because
// evidence.Read/Verify/CopyTo refuse a row without a digest (evidence.ErrNoDigest).
func normalizeEvidence(e model.Evidence) (model.Evidence, error) {
	e.Digest = strings.ToLower(strings.TrimSpace(e.Digest))
	if e.Size < 0 {
		return e, fmt.Errorf("证据 %s 的大小不能为负：%d", evidenceName(e), e.Size)
	}
	if e.Digest != "" && !isSHA256Hex(e.Digest) {
		return e, fmt.Errorf("证据 %s 的摘要不是 SHA-256 hex：%q", evidenceName(e), e.Digest)
	}
	if digest, preserved := evidence.ObjectDigest(e.URI); preserved {
		if e.Digest == "" {
			return e, fmt.Errorf("证据 %s 指向保全副本 %s，却没有记录摘要", evidenceName(e), e.URI)
		}
		if digest != e.Digest {
			return e, fmt.Errorf("证据 %s 的摘要 %s 与保全副本名 %s 不一致", evidenceName(e), e.Digest, e.URI)
		}
	}
	return e, nil
}

// EvidenceVerifiable reports whether a registered original still carries the
// digest its bytes can be checked against. Readiness (internal/api) and the
// dispatch freeze in PrepareRunInput both read this one predicate, so the page
// and the recorded gap describe the same row. It is not a dispatch veto: a row
// that cannot be re-proven is recorded as a gap on the run (which direction, what
// is missing), because an incomplete evidence chain says nothing about whether
// the direction is worth testing.
//
// "Carries a digest" means a digest a reader could actually compare against the
// bytes, so the test is the same SHA-256 shape the write path enforces
// (normalizeEvidence) and the read path applies (compareDigest). Asking only for
// non-emptiness let a row written around the write path — hand-edited ledger,
// import — count as verifiable on both screen and gate while no reader could ever
// prove it. Case is folded before the check because both the write path
// (normalizeEvidence) and the reader (preserve.go, compareDigest) lower-case the
// digest; refusing an upper-case hex digest here would refuse a row the reader can
// verify perfectly well. Do not drop the ToLower as redundant — it is deliberately
// the same fold the other two sides do, and removing it refuses valid rows.
func EvidenceVerifiable(e model.Evidence) bool {
	return isSHA256Hex(strings.ToLower(strings.TrimSpace(e.Digest)))
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func evidenceName(e model.Evidence) string {
	switch {
	case e.ID != "":
		return e.ID
	case e.URI != "":
		return e.URI
	default:
		return "（无 id/uri）"
	}
}
