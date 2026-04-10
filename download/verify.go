package download

import (
	"crypto/sha1"
	"fmt"
	"os"

	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/torrent"
)

// VerifyPieces reads existing file data, hashes each piece, and returns
// a Bitfield of verified pieces along with the count of valid pieces.
// If the file doesn't exist or is too small, returns an empty bitfield.
func VerifyPieces(path string, tf *torrent.TorrentFile) (peer.Bitfield, int, error) {
	numPieces := len(tf.Info.PieceHashes)
	tl := TotalSize(tf)
	pieceLen := int(tf.Info.PieceLength)

	bf := make(peer.Bitfield, (numPieces+7)/8)

	f, err := os.Open(path)
	if err != nil {
		return bf, 0, nil // file doesn't exist, all pieces missing
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return bf, 0, fmt.Errorf("verify: stat failed: %w", err)
	}

	verified := 0
	for i, expectedHash := range tf.Info.PieceHashes {
		offset := int64(i) * int64(pieceLen)
		length := pieceLen
		remaining := int(tl) - i*pieceLen
		if remaining < length {
			length = remaining
		}

		// Skip if file is too small for this piece
		if offset+int64(length) > info.Size() {
			break
		}

		buf := make([]byte, length)
		n, err := f.ReadAt(buf, offset)
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
