package engine

import (
	"testing"

	"github.com/skmtkytr/stor/torrent"
)

// file shorthand for building torrent.File values in tests.
func tfile(length int64) torrent.File {
	return torrent.File{Length: length, Path: []string{"f"}}
}

// assertMaskBits verifies that the given piece indices are set/cleared.
// wantSet: indices that must be set. All other indices in [0, numPieces) must be clear.
func assertMaskBits(t *testing.T, mask []byte, numPieces int, wantSet map[int]bool) {
	t.Helper()
	for i := range numPieces {
		byteIdx := i / 8
		bitOff := 7 - (i % 8)
		have := byteIdx < len(mask) && (mask[byteIdx]>>bitOff)&1 != 0
		want := wantSet[i]
		if have != want {
			t.Errorf("piece %d: got set=%v, want set=%v", i, have, want)
		}
	}
}

func TestComputeSkippedPieceMaskSingleFileAllSkipped(t *testing.T) {
	// Single-file torrent: 3 pieces, piece length 100, total 250.
	// If the only file is skipped, every piece is fully inside it and must be
	// marked for skip.
	files := []torrent.File{tfile(250)}
	prio := []int8{-1}
	mask := ComputeSkippedPieceMask(files, prio, 100, 250, 3)
	assertMaskBits(t, mask, 3, map[int]bool{0: true, 1: true, 2: true})
}

func TestComputeSkippedPieceMaskSingleFileNotSkipped(t *testing.T) {
	files := []torrent.File{tfile(250)}
	prio := []int8{0}
	mask := ComputeSkippedPieceMask(files, prio, 100, 250, 3)
	assertMaskBits(t, mask, 3, map[int]bool{})
}

func TestComputeSkippedPieceMaskNilPriorities(t *testing.T) {
	// Nil priorities means "all normal" → no mask bits set.
	files := []torrent.File{tfile(250)}
	mask := ComputeSkippedPieceMask(files, nil, 100, 250, 3)
	assertMaskBits(t, mask, 3, map[int]bool{})
}

func TestComputeSkippedPieceMaskFileSpansTwoPiecesNeighborWanted(t *testing.T) {
	// 3 pieces of 100 B, total 300.
	// File A: [0, 100) — piece 0 exactly.       skipped
	// File B: [100, 250) — pieces 1 (full), 2 partial (100 B of 100).
	//         Actually: piece 2 covers bytes [200, 300). File B ends at 250,
	//         so piece 2 straddles File B (50 B) and File C (50 B).
	// File C: [250, 300) — last 50 B of piece 2. wanted
	//
	// Only piece 0 is fully inside a skipped file → mask bit 0 set.
	files := []torrent.File{tfile(100), tfile(150), tfile(50)}
	prio := []int8{-1, 0, 0}
	mask := ComputeSkippedPieceMask(files, prio, 100, 300, 3)
	assertMaskBits(t, mask, 3, map[int]bool{0: true})
}

func TestComputeSkippedPieceMaskStraddlingPieceNotSkipped(t *testing.T) {
	// 2 pieces of 100 B, total 200.
	// File A: [0, 150) — covers piece 0 fully and half of piece 1. skipped
	// File B: [150, 200) — last 50 B of piece 1.                    wanted
	// Piece 0 is fully inside File A → skip.
	// Piece 1 straddles A (skipped) and B (wanted) → NOT skip.
	files := []torrent.File{tfile(150), tfile(50)}
	prio := []int8{-1, 0}
	mask := ComputeSkippedPieceMask(files, prio, 100, 200, 2)
	assertMaskBits(t, mask, 2, map[int]bool{0: true})
}

func TestComputeSkippedPieceMaskFileInsideSinglePiece(t *testing.T) {
	// 1 piece of 1000 B, two files inside: [0,500) and [500,1000).
	// Skipping only one of them must not skip the piece (it's not fully in
	// skipped files — the other half is wanted).
	files := []torrent.File{tfile(500), tfile(500)}
	prio := []int8{-1, 0}
	mask := ComputeSkippedPieceMask(files, prio, 1000, 1000, 1)
	assertMaskBits(t, mask, 1, map[int]bool{})
}

func TestComputeSkippedPieceMaskBothFilesInSinglePieceSkipped(t *testing.T) {
	// 1 piece, two files, both skipped → piece is fully inside skipped files.
	files := []torrent.File{tfile(500), tfile(500)}
	prio := []int8{-1, -1}
	mask := ComputeSkippedPieceMask(files, prio, 1000, 1000, 1)
	assertMaskBits(t, mask, 1, map[int]bool{0: true})
}

func TestComputeSkippedPieceMaskLastPieceShorter(t *testing.T) {
	// piece length 1000, total 2500 (so piece 2 is only 500 B).
	// File A: [0, 2000) — pieces 0, 1 fully.           wanted
	// File B: [2000, 2500) — piece 2 (500 B) fully.    skipped
	// Piece 2 is fully inside File B → skip bit 2.
	files := []torrent.File{tfile(2000), tfile(500)}
	prio := []int8{0, -1}
	mask := ComputeSkippedPieceMask(files, prio, 1000, 2500, 3)
	assertMaskBits(t, mask, 3, map[int]bool{2: true})
}

func TestComputeSkippedPieceMaskAllFilesSkipped(t *testing.T) {
	files := []torrent.File{tfile(100), tfile(100), tfile(100)}
	prio := []int8{-1, -1, -1}
	mask := ComputeSkippedPieceMask(files, prio, 100, 300, 3)
	assertMaskBits(t, mask, 3, map[int]bool{0: true, 1: true, 2: true})
}

func TestComputeSkippedPieceMaskPrioritiesShorterThanFiles(t *testing.T) {
	// Priorities array shorter than files → trailing files default to normal.
	files := []torrent.File{tfile(100), tfile(100), tfile(100)}
	prio := []int8{-1} // only first file explicit
	mask := ComputeSkippedPieceMask(files, prio, 100, 300, 3)
	assertMaskBits(t, mask, 3, map[int]bool{0: true})
}

func TestComputeSkippedPieceMaskEmptyFiles(t *testing.T) {
	// No files → empty mask (no pieces to skip).
	mask := ComputeSkippedPieceMask(nil, nil, 100, 0, 0)
	if len(mask) != 0 {
		t.Fatalf("expected empty mask, got %v", mask)
	}
}
