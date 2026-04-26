package engine

import (
	"testing"

	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/torrent"
)

// makeBitfield returns a freshly-allocated bitfield of the right size for
// numPieces with the listed pieces set.
func makeBitfield(numPieces int, set ...int) peer.Bitfield {
	bf := make(peer.Bitfield, (numPieces+7)/8)
	for _, i := range set {
		bf.SetPiece(i)
	}
	return bf
}

// TestComputeFileRangesSingleFile: a single-file torrent yields one entry
// covering the entire piece range and using info.Name as the path.
func TestComputeFileRangesSingleFile(t *testing.T) {
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        "movie.mkv",
			PieceLength: 256,
			Length:      1000,
			PieceHashes: make([][20]byte, 4), // ceil(1000/256)
		},
	}
	got := computeFileRanges(tf)
	if len(got) != 1 {
		t.Fatalf("expected 1 file range, got %d", len(got))
	}
	want := fileRangeEntry{index: 0, path: "movie.mkv", size: 1000, firstPiece: 0, lastPiece: 3}
	if got[0] != want {
		t.Errorf("got %+v, want %+v", got[0], want)
	}
}

// TestComputeFileRangesMultiFile: each file maps to the contiguous piece
// range that contains its bytes; pieces straddling file boundaries belong
// to both files (firstPiece of file N+1 may equal lastPiece of file N).
func TestComputeFileRangesMultiFile(t *testing.T) {
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        "release",
			PieceLength: 256,
			PieceHashes: make([][20]byte, 4), // covers up to 1024 bytes
			Files: []torrent.File{
				{Length: 200, Path: []string{"a.txt"}},        // bytes 0..199 (piece 0)
				{Length: 600, Path: []string{"sub", "b.bin"}}, // bytes 200..799 (pieces 0..3)
				{Length: 100, Path: []string{"c.dat"}},        // bytes 800..899 (piece 3)
			},
		},
	}
	got := computeFileRanges(tf)
	if len(got) != 3 {
		t.Fatalf("expected 3 file ranges, got %d", len(got))
	}

	want := []fileRangeEntry{
		{index: 0, path: "a.txt", size: 200, firstPiece: 0, lastPiece: 0},
		{index: 1, path: "sub/b.bin", size: 600, firstPiece: 0, lastPiece: 3},
		{index: 2, path: "c.dat", size: 100, firstPiece: 3, lastPiece: 3},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestEventFileCompletedSingleFile verifies that all-pieces-complete on a
// single-file torrent surfaces exactly one FileCompleted entry from the
// helper that drives OnPiece.
func TestEventFileCompletedSingleFile(t *testing.T) {
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        "movie.mkv",
			PieceLength: 256,
			Length:      1000,
			PieceHashes: make([][20]byte, 4),
		},
	}
	ranges := computeFileRanges(tf)
	s := &Session{}

	bf := makeBitfield(4, 0, 1, 2) // missing piece 3
	if got := s.collectNewlyCompletedFiles(ranges, bf); len(got) != 0 {
		t.Fatalf("expected no completion before final piece, got %v", got)
	}

	bf.SetPiece(3)
	got := s.collectNewlyCompletedFiles(ranges, bf)
	if len(got) != 1 || got[0].index != 0 || got[0].path != "movie.mkv" || got[0].size != 1000 {
		t.Fatalf("expected single completion for index 0, got %+v", got)
	}
}

// TestEventFileCompletedMultiFile verifies file boundaries: completing
// pieces for file 0 only reports file 0; later pieces cascade into the
// shared piece for file 1 (which still needs all of its piece range).
func TestEventFileCompletedMultiFile(t *testing.T) {
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        "release",
			PieceLength: 256,
			PieceHashes: make([][20]byte, 4),
			Files: []torrent.File{
				{Length: 200, Path: []string{"a.txt"}},        // piece 0 only
				{Length: 600, Path: []string{"sub", "b.bin"}}, // pieces 0..3
				{Length: 100, Path: []string{"c.dat"}},        // piece 3 only
			},
		},
	}
	ranges := computeFileRanges(tf)
	s := &Session{}

	// Complete piece 0: only "a.txt" is fully covered (b.bin needs 1..3 too;
	// c.dat needs 3). Expect a single FileCompleted for index 0.
	bf := makeBitfield(4, 0)
	got := s.collectNewlyCompletedFiles(ranges, bf)
	if len(got) != 1 || got[0].index != 0 {
		t.Fatalf("after piece 0 expected only file 0 done, got %+v", got)
	}

	// Complete pieces 1, 2: nothing new (b.bin still needs 3, c.dat needs 3).
	bf.SetPiece(1)
	bf.SetPiece(2)
	got = s.collectNewlyCompletedFiles(ranges, bf)
	if len(got) != 0 {
		t.Fatalf("expected no new completions after pieces 1,2: got %+v", got)
	}

	// Complete piece 3: both b.bin and c.dat are now fully covered.
	bf.SetPiece(3)
	got = s.collectNewlyCompletedFiles(ranges, bf)
	if len(got) != 2 {
		t.Fatalf("expected 2 new completions after final piece, got %+v", got)
	}
	idxSeen := map[int]bool{}
	for _, fr := range got {
		idxSeen[fr.index] = true
	}
	if !idxSeen[1] || !idxSeen[2] {
		t.Errorf("expected file indices 1 and 2, got %v", idxSeen)
	}
}

// TestEventFileCompletedNoDuplicate verifies the helper records each
// completion in s.completedFiles so a redundant call returns nothing.
func TestEventFileCompletedNoDuplicate(t *testing.T) {
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        "movie.mkv",
			PieceLength: 256,
			Length:      1000,
			PieceHashes: make([][20]byte, 4),
		},
	}
	ranges := computeFileRanges(tf)
	s := &Session{}
	bf := makeBitfield(4, 0, 1, 2, 3)

	first := s.collectNewlyCompletedFiles(ranges, bf)
	if len(first) != 1 {
		t.Fatalf("first call: expected 1 completion, got %d", len(first))
	}
	second := s.collectNewlyCompletedFiles(ranges, bf)
	if len(second) != 0 {
		t.Fatalf("second call must not duplicate: got %v", second)
	}
}

// TestEventFileCompletedSeedingResumeSuppression verifies the resume path:
// if the session was restarted with a bitfield that already had file 0
// fully present, the OnPiece path must NOT emit a FileCompleted event for
// it (the user already knew about that completion in the previous run).
func TestEventFileCompletedSeedingResumeSuppression(t *testing.T) {
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        "movie.mkv",
			PieceLength: 256,
			Length:      1000,
			PieceHashes: make([][20]byte, 4),
		},
	}
	ranges := computeFileRanges(tf)
	s := &Session{
		completedFiles: map[int]bool{0: true}, // pretend resume saw it complete
	}

	bf := makeBitfield(4, 0, 1, 2, 3)
	if got := s.collectNewlyCompletedFiles(ranges, bf); len(got) != 0 {
		t.Fatalf("expected suppression for pre-completed file, got %v", got)
	}
}
