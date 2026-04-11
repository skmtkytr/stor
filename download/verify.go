package download

import (
	"crypto/sha1"
	"fmt"
	"os"

	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/torrent"
)

// readAtReader abstracts single-file and multi-file read targets.
type readAtReader interface {
	ReadAt([]byte, int64) (int, error)
}

// VerifyPieces reads existing file data, hashes each piece, and returns
// a Bitfield of verified pieces along with the count of valid pieces.
// Supports both single-file and multi-file torrents.
func VerifyPieces(path string, tf *torrent.TorrentFile) (peer.Bitfield, int, error) {
	numPieces := len(tf.Info.PieceHashes)
	tl := TotalSize(tf)
	pieceLen := int(tf.Info.PieceLength)

	bf := make(peer.Bitfield, (numPieces+7)/8)

	var r readAtReader
	if IsMultiFile(tf) {
		mw, err := NewMultiFileWriter(path, tf)
		if err != nil {
			return bf, 0, nil // directory doesn't exist
		}
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

	verified := 0
	for i, expectedHash := range tf.Info.PieceHashes {
		offset := int64(i) * int64(pieceLen)
		length := pieceLen
		remaining := int(tl) - i*pieceLen
		if remaining < length {
			length = remaining
		}

		buf := make([]byte, length)
		n, err := r.ReadAt(buf, offset)
		if err != nil || n != length {
			continue
		}

		hash := sha1.Sum(buf)
		if hash == expectedHash {
			bf.SetPiece(i)
			verified++
		}
	}

	return bf, verified, nil
}
