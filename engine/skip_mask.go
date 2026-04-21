package engine

import (
	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/storage"
	"github.com/skmtkytr/stor/torrent"
)

// Priority constants for FileEntry / TorrentRecord.FilePriorities.
const (
	PriorityNormal int8 = 0
	PrioritySkip   int8 = -1
)

// ComputeSkippedPieceMask returns a bitfield where each set bit marks a
// piece whose entire byte range falls inside one or more files whose
// priority is PrioritySkip. Such pieces must not be queued for download.
//
// A piece that straddles any wanted (non-skip) file is NOT marked — we
// can't download a partial piece, so we have to take the whole one.
//
// priorities is indexed by file order. Entries beyond len(priorities) are
// treated as PriorityNormal. Passing a nil or all-normal slice yields an
// empty mask (no pieces to skip).
//
// For single-file torrents (len(files) == 0 but Info.Length > 0), callers
// should synthesise a single-element files slice from the top-level length
// so this function can operate uniformly.
func ComputeSkippedPieceMask(files []torrent.File, priorities []int8, pieceLength, totalBytes int64, numPieces int) peer.Bitfield {
	if numPieces <= 0 || pieceLength <= 0 || len(files) == 0 {
		return nil
	}

	// Effective piece mask size (rounded up to byte).
	mask := make(peer.Bitfield, (numPieces+7)/8)

	// Build a [start, end) range for each file.
	type fileRange struct {
		start, end int64
		skip       bool
	}
	ranges := make([]fileRange, len(files))
	var offset int64
	for i, f := range files {
		skip := false
		if i < len(priorities) && priorities[i] == PrioritySkip {
			skip = true
		}
		ranges[i] = fileRange{start: offset, end: offset + f.Length, skip: skip}
		offset += f.Length
	}

	// For each piece, determine if ALL of its bytes lie inside files that
	// are marked skip. Walk ranges covering the piece and if any wanted
	// (non-skip) range intersects the piece, the piece is not skippable.
	for i := range numPieces {
		pieceStart := int64(i) * pieceLength
		pieceEnd := pieceStart + pieceLength
		if pieceEnd > totalBytes {
			pieceEnd = totalBytes
		}
		if pieceEnd <= pieceStart {
			continue
		}

		allSkipped := true
		coveredAny := false
		for _, r := range ranges {
			if r.end <= pieceStart || r.start >= pieceEnd {
				continue // no overlap
			}
			coveredAny = true
			if !r.skip {
				allSkipped = false
				break
			}
		}
		if coveredAny && allSkipped {
			mask.SetPiece(i)
		}
	}

	return mask
}

// computeSkipMaskFromRecord builds a skip mask for the given torrent file
// using the per-file priorities from the record. For single-file torrents
// it synthesises a single-element []torrent.File from Info.Length so
// ComputeSkippedPieceMask can operate uniformly. Returns nil when no
// priorities are set (fast path for the common case).
func computeSkipMaskFromRecord(tf *torrent.TorrentFile, priorities []int8) peer.Bitfield {
	if tf == nil || len(priorities) == 0 {
		return nil
	}
	// Fast path: if nothing is marked skip, we skip nothing.
	any := false
	for _, p := range priorities {
		if p == PrioritySkip {
			any = true
			break
		}
	}
	if !any {
		return nil
	}

	files := tf.Info.Files
	if len(files) == 0 {
		// Single-file torrent: synthesise a one-entry slice.
		files = []torrent.File{{Length: tf.Info.Length, Path: []string{tf.Info.Name}}}
	}

	numPieces := len(tf.Info.PieceHashes)
	pieceLength := tf.Info.PieceLength
	totalBytes := storage.TotalSize(tf)
	return ComputeSkippedPieceMask(files, priorities, pieceLength, totalBytes, numPieces)
}
