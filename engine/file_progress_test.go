package engine

import (
	"testing"

	"github.com/skmtkytr/stor/peer"
)

func TestComputeFileDownloadedSinglePiece(t *testing.T) {
	// 1 piece of 1000 B covering one 1000-byte file.
	bf := peer.Bitfield{0x80} // piece 0 set
	got := computeFileDownloaded(bf, 0, 1000, 1000, 1000, 1)
	if got != 1000 {
		t.Fatalf("got %d, want 1000", got)
	}
}

func TestComputeFileDownloadedNoPiecesYet(t *testing.T) {
	bf := peer.Bitfield{0x00}
	got := computeFileDownloaded(bf, 0, 1000, 1000, 1000, 1)
	if got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestComputeFileDownloadedPartialPiecesCoveringFile(t *testing.T) {
	// 4 pieces of 1000 B each, total 4000 B.
	// File [0, 2500) needs pieces 0, 1, 2 (partial for piece 2).
	// bf has pieces 0 and 2 set, missing piece 1.
	bf := peer.Bitfield{0b10100000}
	got := computeFileDownloaded(bf, 0, 2500, 1000, 4000, 4)
	// piece 0 gives 1000; piece 2 gives 500 (overlap with file ends at 2500)
	if got != 1500 {
		t.Fatalf("got %d, want 1500 (1000 from piece 0 + 500 from piece 2)", got)
	}
}

func TestComputeFileDownloadedFileInsidePiece(t *testing.T) {
	// 1 piece of 10000 B, file at offset 1000 length 500 (entirely inside piece 0).
	bf := peer.Bitfield{0x80}
	got := computeFileDownloaded(bf, 1000, 500, 10000, 10000, 1)
	if got != 500 {
		t.Fatalf("got %d, want 500", got)
	}
}

func TestComputeFileDownloadedLastPieceSmaller(t *testing.T) {
	// Total 2500 B, piece length 1000. Piece 2 is only 500 B.
	// File [2000, 2500) is entirely piece 2.
	bf := peer.Bitfield{0b00100000} // piece 2 set
	got := computeFileDownloaded(bf, 2000, 500, 1000, 2500, 3)
	if got != 500 {
		t.Fatalf("got %d, want 500", got)
	}
}

func TestComputeFileDownloadedNilBitfield(t *testing.T) {
	got := computeFileDownloaded(nil, 0, 1000, 1000, 1000, 1)
	if got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestComputeFileDownloadedFileSpanningTwoPiecesOnlyFirstDone(t *testing.T) {
	// 2 pieces of 1000 B. File [500, 1500) spans both.
	// Only piece 0 complete → file gets 500 B (from piece 0's 500..1000 overlap).
	bf := peer.Bitfield{0b10000000}
	got := computeFileDownloaded(bf, 500, 1000, 1000, 2000, 2)
	if got != 500 {
		t.Fatalf("got %d, want 500", got)
	}
}
