package engine

import "github.com/skmtkytr/stor/peer"

// computeFileDownloaded returns how many bytes of a file are covered by
// complete pieces, where the file occupies the byte range
// [offset, offset+length) within the torrent.
//
// pieceLength is the nominal piece length; totalBytes is the whole-torrent
// byte count (needed because the last piece may be shorter). numPieces is
// the piece count; pieces >= numPieces are ignored.
//
// Returns 0 when bf is nil or length <= 0.
func computeFileDownloaded(bf peer.Bitfield, offset, length, pieceLength, totalBytes int64, numPieces int) int64 {
	if bf == nil || length <= 0 || pieceLength <= 0 {
		return 0
	}
	end := offset + length
	firstPiece := int(offset / pieceLength)
	lastPiece := int((end - 1) / pieceLength)
	if lastPiece >= numPieces {
		lastPiece = numPieces - 1
	}

	var downloaded int64
	for i := firstPiece; i <= lastPiece; i++ {
		if !bf.HasPiece(i) {
			continue
		}
		pieceStart := int64(i) * pieceLength
		pieceEnd := pieceStart + pieceLength
		if pieceEnd > totalBytes {
			pieceEnd = totalBytes
		}
		start := pieceStart
		if start < offset {
			start = offset
		}
		finish := pieceEnd
		if finish > end {
			finish = end
		}
		if finish > start {
			downloaded += finish - start
		}
	}
	return downloaded
}
