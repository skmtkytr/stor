package torrent

import (
	"crypto/sha1"
	"strings"
	"testing"

	"github.com/skmtkytr/stor/bencode"
)

func buildTestTorrent(t *testing.T, info map[string]any, extra map[string]any) []byte {
	t.Helper()
	d := map[string]any{
		"announce": "http://tracker.example.com/announce",
		"info":     info,
	}
	for k, v := range extra {
		d[k] = v
	}
	data, err := bencode.Encode(d)
	if err != nil {
		t.Fatalf("failed to encode test torrent: %v", err)
	}
	return data
}

func fakePieces(n int) string {
	// Each piece hash is 20 bytes (SHA1)
	var b strings.Builder
	for i := range n {
		h := sha1.Sum([]byte{byte(i)})
		b.Write(h[:])
	}
	return b.String()
}

func TestParseSingleFile(t *testing.T) {
	info := map[string]any{
		"name":         "test.txt",
		"piece length": int64(262144),
		"pieces":       fakePieces(2),
		"length":       int64(524288),
	}
	data := buildTestTorrent(t, info, nil)

	tf, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tf.Announce != "http://tracker.example.com/announce" {
		t.Errorf("announce: got %q", tf.Announce)
	}
	if tf.Info.Name != "test.txt" {
		t.Errorf("name: got %q", tf.Info.Name)
	}
	if tf.Info.PieceLength != 262144 {
		t.Errorf("piece length: got %d", tf.Info.PieceLength)
	}
	if len(tf.Info.PieceHashes) != 2 {
		t.Errorf("piece hashes: got %d, want 2", len(tf.Info.PieceHashes))
	}
	if tf.Info.Length != 524288 {
		t.Errorf("length: got %d", tf.Info.Length)
	}
	if len(tf.Info.Files) != 0 {
		t.Errorf("files: got %d, want 0 for single file", len(tf.Info.Files))
	}
	// InfoHash should be 20 bytes
	if len(tf.InfoHash) != 20 {
		t.Errorf("info hash length: got %d, want 20", len(tf.InfoHash))
	}
}

func TestParseMultiFile(t *testing.T) {
	info := map[string]any{
		"name":         "my-folder",
		"piece length": int64(262144),
		"pieces":       fakePieces(3),
		"files": []any{
			map[string]any{
				"length": int64(100000),
				"path":   []any{"subdir", "file1.txt"},
			},
			map[string]any{
				"length": int64(200000),
				"path":   []any{"file2.txt"},
			},
		},
	}
	data := buildTestTorrent(t, info, nil)

	tf, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tf.Info.Name != "my-folder" {
		t.Errorf("name: got %q", tf.Info.Name)
	}
	if len(tf.Info.Files) != 2 {
		t.Fatalf("files: got %d, want 2", len(tf.Info.Files))
	}
	if tf.Info.Files[0].Length != 100000 {
		t.Errorf("file[0] length: got %d", tf.Info.Files[0].Length)
	}
	if len(tf.Info.Files[0].Path) != 2 || tf.Info.Files[0].Path[0] != "subdir" {
		t.Errorf("file[0] path: got %v", tf.Info.Files[0].Path)
	}
	if tf.Info.Files[1].Length != 200000 {
		t.Errorf("file[1] length: got %d", tf.Info.Files[1].Length)
	}
}

func TestParseAnnounceList(t *testing.T) {
	info := map[string]any{
		"name":         "test.txt",
		"piece length": int64(262144),
		"pieces":       fakePieces(1),
		"length":       int64(100),
	}
	extra := map[string]any{
		"announce-list": []any{
			[]any{"http://tracker1.example.com/announce", "http://tracker2.example.com/announce"},
			[]any{"http://tracker3.example.com/announce"},
		},
	}
	data := buildTestTorrent(t, info, extra)

	tf, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(tf.AnnounceList) != 2 {
		t.Fatalf("announce-list tiers: got %d, want 2", len(tf.AnnounceList))
	}
	if len(tf.AnnounceList[0]) != 2 {
		t.Errorf("tier 0: got %d trackers, want 2", len(tf.AnnounceList[0]))
	}
}

func TestParseInvalidPiecesLength(t *testing.T) {
	info := map[string]any{
		"name":         "test.txt",
		"piece length": int64(262144),
		"pieces":       "not-multiple-of-20",
		"length":       int64(100),
	}
	data := buildTestTorrent(t, info, nil)

	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected error for invalid pieces length")
	}
}

func TestParseMissingInfo(t *testing.T) {
	d := map[string]any{
		"announce": "http://tracker.example.com/announce",
	}
	data, _ := bencode.Encode(d)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected error for missing info")
	}
}

func TestInfoHashConsistency(t *testing.T) {
	info := map[string]any{
		"name":         "test.txt",
		"piece length": int64(262144),
		"pieces":       fakePieces(1),
		"length":       int64(100),
	}
	data := buildTestTorrent(t, info, nil)

	tf1, _ := Parse(data)
	tf2, _ := Parse(data)

	if tf1.InfoHash != tf2.InfoHash {
		t.Error("info hash should be deterministic")
	}
}

func TestParseWebSeedURLs(t *testing.T) {
	info := map[string]any{
		"name":         "test.txt",
		"piece length": int64(262144),
		"pieces":       fakePieces(1),
		"length":       int64(100),
	}

	// Single URL string
	data := buildTestTorrent(t, info, map[string]any{
		"url-list": "http://example.com/test.txt",
	})
	tf, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(tf.WebSeedURLs) != 1 || tf.WebSeedURLs[0] != "http://example.com/test.txt" {
		t.Errorf("single url-list: got %v", tf.WebSeedURLs)
	}

	// List of URLs
	data = buildTestTorrent(t, info, map[string]any{
		"url-list": []any{"http://a.com/file", "http://b.com/file"},
	})
	tf, err = Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(tf.WebSeedURLs) != 2 {
		t.Errorf("list url-list: got %d URLs, want 2", len(tf.WebSeedURLs))
	}

	// No url-list
	data = buildTestTorrent(t, info, nil)
	tf, err = Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(tf.WebSeedURLs) != 0 {
		t.Errorf("no url-list: got %v", tf.WebSeedURLs)
	}
}

func TestParseHybridTorrent(t *testing.T) {
	// Hybrid torrent has both v1 pieces and v2 file tree
	info := map[string]any{
		"name":         "hybrid.txt",
		"piece length": int64(262144),
		"pieces":       fakePieces(2),
		"length":       int64(524288),
		"meta version": int64(2),
		"file tree": map[string]any{
			"hybrid.txt": map[string]any{
				"": map[string]any{
					"length":      int64(524288),
					"pieces root": "0123456789abcdef0123456789abcdef",
				},
			},
		},
	}
	data := buildTestTorrent(t, info, nil)

	tf, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tf.MetaVersion != 2 {
		t.Errorf("MetaVersion: got %d, want 2", tf.MetaVersion)
	}
	// v1 fields should still be parsed
	if len(tf.Info.PieceHashes) != 2 {
		t.Errorf("PieceHashes: got %d, want 2", len(tf.Info.PieceHashes))
	}
	// InfoHashV2 should be non-zero
	var zero [32]byte
	if tf.InfoHashV2 == zero {
		t.Error("InfoHashV2 should be non-zero for hybrid torrent")
	}
	// FileTree should be stored
	if tf.Info.FileTree == nil {
		t.Error("FileTree should be stored for hybrid torrent")
	}
}

func TestParseV2OnlyTorrentErrors(t *testing.T) {
	// v2-only torrent has file tree but no pieces key
	info := map[string]any{
		"name":         "v2only.txt",
		"piece length": int64(262144),
		"meta version": int64(2),
		"file tree": map[string]any{
			"v2only.txt": map[string]any{
				"": map[string]any{
					"length":      int64(100),
					"pieces root": "0123456789abcdef0123456789abcdef",
				},
			},
		},
	}
	data := buildTestTorrent(t, info, nil)

	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected error for v2-only torrent")
	}
	if !strings.Contains(err.Error(), "v2-only") {
		t.Errorf("error should mention v2-only, got: %v", err)
	}
}

func TestParsePrivateTorrent(t *testing.T) {
	info := map[string]any{
		"name":         "private.txt",
		"piece length": int64(262144),
		"pieces":       fakePieces(1),
		"length":       int64(100),
		"private":      int64(1),
	}
	data := buildTestTorrent(t, info, nil)

	tf, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tf.Info.Private {
		t.Error("expected Private=true for private:1")
	}
}

func TestParseNonPrivateTorrent(t *testing.T) {
	info := map[string]any{
		"name":         "public.txt",
		"piece length": int64(262144),
		"pieces":       fakePieces(1),
		"length":       int64(100),
	}
	data := buildTestTorrent(t, info, nil)

	tf, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tf.Info.Private {
		t.Error("expected Private=false when private key absent")
	}
}

func TestParseRejectsZeroPieceLength(t *testing.T) {
	info := map[string]any{
		"name":         "bad.txt",
		"piece length": int64(0),
		"pieces":       fakePieces(1),
		"length":       int64(100),
	}
	data := buildTestTorrent(t, info, nil)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected error for zero piece length")
	}
}

func TestParseRejectsNegativePieceLength(t *testing.T) {
	info := map[string]any{
		"name":         "bad.txt",
		"piece length": int64(-256),
		"pieces":       fakePieces(1),
		"length":       int64(100),
	}
	data := buildTestTorrent(t, info, nil)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected error for negative piece length")
	}
}

func TestParseRejectsNegativeFileLength(t *testing.T) {
	info := map[string]any{
		"name":         "bad.txt",
		"piece length": int64(256),
		"pieces":       fakePieces(1),
		"length":       int64(-100),
	}
	data := buildTestTorrent(t, info, nil)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected error for negative file length")
	}
}
