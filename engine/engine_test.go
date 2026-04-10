package engine

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/skmtkytr/stor/bencode"
)

func buildTestTorrentData(t *testing.T, name string, pieceLen int, dataLen int) ([]byte, [20]byte) {
	t.Helper()
	pieces := make([]byte, 0)
	remaining := dataLen
	for remaining > 0 {
		size := pieceLen
		if remaining < size {
			size = remaining
		}
		chunk := make([]byte, size)
		h := sha1.Sum(chunk)
		pieces = append(pieces, h[:]...)
		remaining -= size
	}

	info := map[string]any{
		"name":         name,
		"piece length": int64(pieceLen),
		"pieces":       string(pieces),
		"length":       int64(dataLen),
	}
	d := map[string]any{
		"announce": "http://tracker.example.com/announce",
		"info":     info,
	}
	data, err := bencode.Encode(d)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Compute info hash
	infoData, _ := bencode.Encode(info)
	infoHash := sha1.Sum(infoData)
	return data, infoHash
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		DownloadDir: filepath.Join(dir, "downloads"),
		StatePath:   filepath.Join(dir, "state.json"),
		ListenPort:  6881,
		MaxActive:   3,
	}
	eng, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = eng.Stop() })
	return eng
}

func TestEngineAddAndList(t *testing.T) {
	eng := newTestEngine(t)

	// Write a .torrent file
	data, infoHash := buildTestTorrentData(t, "test.txt", 256, 512)
	torrentPath := filepath.Join(t.TempDir(), "test.torrent")
	if err := os.WriteFile(torrentPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	id, err := eng.AddTorrent(torrentPath)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}

	expectedID := hex.EncodeToString(infoHash[:])
	if id != expectedID {
		t.Errorf("id: got %s, want %s", id, expectedID)
	}

	torrents := eng.ListTorrents()
	if len(torrents) != 1 {
		t.Fatalf("list: got %d, want 1", len(torrents))
	}
	if torrents[0].Name != "test.txt" {
		t.Errorf("name: got %q", torrents[0].Name)
	}
}

func TestEngineAddDuplicate(t *testing.T) {
	eng := newTestEngine(t)

	data, _ := buildTestTorrentData(t, "dup.txt", 256, 512)
	path := filepath.Join(t.TempDir(), "dup.torrent")
	_ = os.WriteFile(path, data, 0o644)

	id1, _ := eng.AddTorrent(path)
	id2, _ := eng.AddTorrent(path)

	if id1 != id2 {
		t.Error("duplicate add should return same ID")
	}
	if len(eng.ListTorrents()) != 1 {
		t.Error("should have 1 torrent, not 2")
	}
}

func TestEngineRemove(t *testing.T) {
	eng := newTestEngine(t)

	data, _ := buildTestTorrentData(t, "rm.txt", 256, 256)
	path := filepath.Join(t.TempDir(), "rm.torrent")
	_ = os.WriteFile(path, data, 0o644)

	id, _ := eng.AddTorrent(path)
	if err := eng.RemoveTorrent(id, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(eng.ListTorrents()) != 0 {
		t.Error("should be empty after remove")
	}
}

func TestEngineRemoveNotFound(t *testing.T) {
	eng := newTestEngine(t)
	if err := eng.RemoveTorrent("nonexistent", false); err == nil {
		t.Error("expected error for nonexistent torrent")
	}
}

func TestEnginePauseResume(t *testing.T) {
	eng := newTestEngine(t)

	data, _ := buildTestTorrentData(t, "pause.txt", 256, 256)
	path := filepath.Join(t.TempDir(), "pause.torrent")
	_ = os.WriteFile(path, data, 0o644)

	id, _ := eng.AddTorrent(path)

	// Give the session a moment to start
	time.Sleep(100 * time.Millisecond)

	if err := eng.PauseTorrent(id); err != nil {
		t.Fatalf("pause: %v", err)
	}

	info, _ := eng.GetTorrent(id)
	if info.State != StatePaused {
		t.Errorf("state after pause: %s", info.State)
	}

	if err := eng.ResumeTorrent(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
}

func TestEngineQueuePosition(t *testing.T) {
	eng := newTestEngine(t)

	var ids []string
	for i := range 5 {
		data, _ := buildTestTorrentData(t, "q"+string(rune('A'+i))+".txt", 256, 256)
		path := filepath.Join(t.TempDir(), "q"+string(rune('A'+i))+".torrent")
		_ = os.WriteFile(path, data, 0o644)
		id, _ := eng.AddTorrent(path)
		ids = append(ids, id)
		time.Sleep(10 * time.Millisecond)
	}

	// Pause all so we can test queue ordering
	for _, id := range ids {
		_ = eng.PauseTorrent(id)
	}
	time.Sleep(50 * time.Millisecond)

	// ids[4] should be at position 4
	info, _ := eng.GetTorrent(ids[4])
	if info.QueuePosition != 4 {
		t.Errorf("initial position of last: got %d, want 4", info.QueuePosition)
	}

	// Move last to top
	_ = eng.QueueTop(ids[4])
	info, _ = eng.GetTorrent(ids[4])
	if info.QueuePosition != 0 {
		t.Errorf("after QueueTop: got %d, want 0", info.QueuePosition)
	}

	// Move first (now at position 1) to bottom
	_ = eng.QueueBottom(ids[0])
	info, _ = eng.GetTorrent(ids[0])
	if info.QueuePosition != 4 {
		t.Errorf("after QueueBottom: got %d, want 4", info.QueuePosition)
	}
}

func TestEngineMaxActive(t *testing.T) {
	eng := newTestEngine(t)

	eng.SetMaxActive(10)
	if eng.MaxActive() != 10 {
		t.Errorf("MaxActive: got %d, want 10", eng.MaxActive())
	}

	eng.SetMaxActive(0) // should clamp to 1
	if eng.MaxActive() != 1 {
		t.Errorf("MaxActive after 0: got %d, want 1", eng.MaxActive())
	}
}

func TestEngineStats(t *testing.T) {
	eng := newTestEngine(t)

	stats := eng.GetStats()
	if stats.TotalTorrents != 0 {
		t.Errorf("total: got %d", stats.TotalTorrents)
	}
	if stats.MaxActive != 3 {
		t.Errorf("max_active: got %d, want 3", stats.MaxActive)
	}
}

func TestEnginePersistence(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		DownloadDir: filepath.Join(dir, "downloads"),
		StatePath:   filepath.Join(dir, "state.json"),
		ListenPort:  6881,
		MaxActive:   3,
	}

	// Create engine, add torrent, stop
	eng1, _ := New(cfg)
	_ = eng1.Start()

	data, _ := buildTestTorrentData(t, "persist.txt", 256, 256)
	path := filepath.Join(t.TempDir(), "persist.torrent")
	_ = os.WriteFile(path, data, 0o644)
	id, _ := eng1.AddTorrent(path)
	time.Sleep(100 * time.Millisecond)
	_ = eng1.PauseTorrent(id)
	_ = eng1.Stop()

	// Create new engine from same state
	eng2, _ := New(cfg)
	_ = eng2.Start()
	defer func() { _ = eng2.Stop() }()

	torrents := eng2.ListTorrents()
	if len(torrents) != 1 {
		t.Fatalf("after restart: got %d torrents, want 1", len(torrents))
	}
	if torrents[0].ID != id {
		t.Errorf("id mismatch: %s vs %s", torrents[0].ID, id)
	}
}

func TestEngineAddTorrentFile(t *testing.T) {
	eng := newTestEngine(t)

	data, _ := buildTestTorrentData(t, "upload.txt", 256, 512)
	id, err := eng.AddTorrentFile(data)
	if err != nil {
		t.Fatalf("AddTorrentFile: %v", err)
	}
	if id == "" {
		t.Error("id should not be empty")
	}

	torrents := eng.ListTorrents()
	if len(torrents) != 1 {
		t.Fatalf("list: got %d", len(torrents))
	}
}
