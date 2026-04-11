package download

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/skmtkytr/stor/torrent"
)

func TestMultiFileWriterWriteAt(t *testing.T) {
	dir := t.TempDir()

	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        "testdir",
			PieceLength: 8,
			Files: []torrent.File{
				{Length: 5, Path: []string{"a.txt"}},
				{Length: 10, Path: []string{"sub", "b.txt"}},
				{Length: 3, Path: []string{"c.txt"}},
			},
		},
	}

	base := filepath.Join(dir, "testdir")
	mw, err := NewMultiFileWriter(base, tf)
	if err != nil {
		t.Fatal(err)
	}
	if err := mw.PreallocateFiles(); err != nil {
		t.Fatal(err)
	}

	// Write data that spans file boundaries
	// Total: 18 bytes, files at [0:5], [5:15], [15:18]
	data := []byte("AAAAABBBBBBBBBBCCC")
	n, err := mw.WriteAt(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 18 {
		t.Fatalf("wrote %d, want 18", n)
	}

	// Verify a.txt
	got, _ := os.ReadFile(filepath.Join(base, "a.txt"))
	if string(got) != "AAAAA" {
		t.Errorf("a.txt: %q", got)
	}

	// Verify sub/b.txt
	got, _ = os.ReadFile(filepath.Join(base, "sub", "b.txt"))
	if string(got) != "BBBBBBBBBB" {
		t.Errorf("sub/b.txt: %q", got)
	}

	// Verify c.txt
	got, _ = os.ReadFile(filepath.Join(base, "c.txt"))
	if string(got) != "CCC" {
		t.Errorf("c.txt: %q", got)
	}
}

func TestMultiFileWriterReadAt(t *testing.T) {
	dir := t.TempDir()

	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        "testdir",
			PieceLength: 4,
			Files: []torrent.File{
				{Length: 3, Path: []string{"x.txt"}},
				{Length: 5, Path: []string{"y.txt"}},
			},
		},
	}

	base := filepath.Join(dir, "testdir")
	mw, err := NewMultiFileWriter(base, tf)
	if err != nil {
		t.Fatal(err)
	}

	// Write some data
	if _, err := mw.WriteAt([]byte("ABCDEFGH"), 0); err != nil {
		t.Fatal(err)
	}

	// Read across file boundary (offset 2, length 4: "C" from x.txt + "DEF" from y.txt)
	buf := make([]byte, 4)
	n, err := mw.ReadAt(buf, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || string(buf) != "CDEF" {
		t.Fatalf("got %q (n=%d), want CDEF", buf, n)
	}
}

func TestMultiFileWriterPieceSpan(t *testing.T) {
	dir := t.TempDir()

	// Piece length 4, but files are [3, 5] so piece 0 spans the boundary
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        "testdir",
			PieceLength: 4,
			Files: []torrent.File{
				{Length: 3, Path: []string{"first.dat"}},
				{Length: 5, Path: []string{"second.dat"}},
			},
		},
	}

	base := filepath.Join(dir, "testdir")
	mw, err := NewMultiFileWriter(base, tf)
	if err != nil {
		t.Fatal(err)
	}
	if err := mw.PreallocateFiles(); err != nil {
		t.Fatal(err)
	}

	// Piece 0: offset 0, length 4 → bytes 0-3 (spans first.dat[0:3] + second.dat[0:1])
	piece0 := []byte{0x11, 0x22, 0x33, 0x44}
	if _, err := mw.WriteAt(piece0, 0); err != nil {
		t.Fatal(err)
	}

	// Piece 1: offset 4, length 4 → bytes 4-7 (second.dat[1:5])
	piece1 := []byte{0x55, 0x66, 0x77, 0x88}
	if _, err := mw.WriteAt(piece1, 4); err != nil {
		t.Fatal(err)
	}

	// Verify first.dat: [0x11, 0x22, 0x33]
	got, _ := os.ReadFile(filepath.Join(base, "first.dat"))
	if len(got) < 3 || got[0] != 0x11 || got[1] != 0x22 || got[2] != 0x33 {
		t.Errorf("first.dat: %x", got)
	}

	// Verify second.dat: [0x44, 0x55, 0x66, 0x77, 0x88]
	got, _ = os.ReadFile(filepath.Join(base, "second.dat"))
	if len(got) < 5 || got[0] != 0x44 || got[1] != 0x55 || got[2] != 0x66 || got[3] != 0x77 || got[4] != 0x88 {
		t.Errorf("second.dat: %x", got)
	}
}
