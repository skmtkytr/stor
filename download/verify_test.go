package download

import (
	"crypto/sha1"
	"os"
	"path/filepath"
	"testing"

	"github.com/skmtkytr/stor/torrent"
)

func buildTestTorrent(pieceLen int, data []byte) *torrent.TorrentFile {
	numPieces := (len(data) + pieceLen - 1) / pieceLen
	hashes := make([][20]byte, numPieces)
	for i := range numPieces {
		start := i * pieceLen
		end := start + pieceLen
		if end > len(data) {
			end = len(data)
		}
		hashes[i] = sha1.Sum(data[start:end])
	}
	return &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        "test",
			PieceLength: int64(pieceLen),
			Length:      int64(len(data)),
			PieceHashes: hashes,
		},
	}
}

func TestVerifyPiecesAllValid(t *testing.T) {
	data := []byte("aaaabbbbccccdddd") // 16 bytes, 4 pieces of 4 bytes
	tf := buildTestTorrent(4, data)

	dir := t.TempDir()
	path := filepath.Join(dir, "test")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	bf, count, err := VerifyPieces(path, tf)
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("expected 4 verified, got %d", count)
	}
	for i := range 4 {
		if !bf.HasPiece(i) {
			t.Errorf("piece %d should be verified", i)
		}
	}
}

func TestVerifyPiecesPartial(t *testing.T) {
	data := []byte("aaaabbbbccccdddd")
	tf := buildTestTorrent(4, data)

	// Write with piece 1 corrupted
	corrupted := make([]byte, len(data))
	copy(corrupted, data)
	corrupted[4] = 'X' // corrupt piece 1

	dir := t.TempDir()
	path := filepath.Join(dir, "test")
	if err := os.WriteFile(path, corrupted, 0o644); err != nil {
		t.Fatal(err)
	}

	bf, count, err := VerifyPieces(path, tf)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected 3 verified, got %d", count)
	}
	if !bf.HasPiece(0) {
		t.Error("piece 0 should be verified")
	}
	if bf.HasPiece(1) {
		t.Error("piece 1 should NOT be verified")
	}
	if !bf.HasPiece(2) {
		t.Error("piece 2 should be verified")
	}
	if !bf.HasPiece(3) {
		t.Error("piece 3 should be verified")
	}
}

func TestVerifyPiecesFileNotExist(t *testing.T) {
	data := []byte("aaaabbbb")
	tf := buildTestTorrent(4, data)

	bf, count, err := VerifyPieces("/nonexistent/file", tf)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected 0 verified, got %d", count)
	}
	if bf.HasPiece(0) || bf.HasPiece(1) {
		t.Error("no pieces should be verified")
	}
}

func TestVerifyPiecesTruncatedFile(t *testing.T) {
	data := []byte("aaaabbbbccccdddd")
	tf := buildTestTorrent(4, data)

	// Write only first 6 bytes (piece 0 complete, piece 1 incomplete)
	dir := t.TempDir()
	path := filepath.Join(dir, "test")
	if err := os.WriteFile(path, data[:6], 0o644); err != nil {
		t.Fatal(err)
	}

	bf, count, err := VerifyPieces(path, tf)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 verified, got %d", count)
	}
	if !bf.HasPiece(0) {
		t.Error("piece 0 should be verified")
	}
}
