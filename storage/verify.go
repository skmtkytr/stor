package storage

import (
	"crypto/sha1"
	"fmt"
	"os"

	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/torrent"
)

// ReadAtReader abstracts single-file and multi-file read targets.
type ReadAtReader interface {
	ReadAt([]byte, int64) (int, error)
}

// VerifyPieces reads existing file data, hashes each piece, and returns
// a Bitfield of verified pieces along with the count of valid pieces.
// Supports both single-file and multi-file torrents.
// onProgress is called after each piece is checked (may be nil).
func VerifyPieces(path string, tf *torrent.TorrentFile, onProgress func(checked, total int)) (peer.Bitfield, int, error) {
	numPieces := len(tf.Info.PieceHashes)
	tl := TotalSize(tf)
	pieceLen := int(tf.Info.PieceLength)

	bf := make(peer.Bitfield, (numPieces+7)/8)

	var r ReadAtReader
	if IsMultiFile(tf) {
		mw, err := NewMultiFileWriter(path, tf)
		if err != nil {
			return bf, 0, nil // directory doesn't exist
		}
		defer func() { _ = mw.Close() }()
		r = mw
	} else {
		f, err := os.Open(path)
		if err != nil {
			return bf, 0, nil // file doesn't exist
		}
		defer func() { _ = f.Close() }()

		info, err := f.Stat()
		if err != nil {
			return bf, 0, fmt.Errorf("verify: stat failed: %w", err)
		}
		_ = info // size checked per-piece via ReadAt errors
		r = f
	}

	// Reuse buffer across pieces (all but last are the same size)
	buf := make([]byte, pieceLen)

	verified := 0
	for i, expectedHash := range tf.Info.PieceHashes {
		offset := int64(i) * int64(pieceLen)
		length := pieceLen
		remaining := int(tl) - i*pieceLen
		if remaining < length {
			length = remaining
		}

		n, err := r.ReadAt(buf[:length], offset)
		if err == nil && n == length {
			hash := sha1.Sum(buf[:length])
			if hash == expectedHash {
				bf.SetPiece(i)
				verified++
			}
		}

		if onProgress != nil {
			onProgress(i+1, numPieces)
		}
	}

	return bf, verified, nil
}
