package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetFilePrioritySingleFile(t *testing.T) {
	eng := newTestEngine(t)

	data, _ := buildTestTorrentData(t, "solo.bin", 256, 512)
	id, err := eng.AddTorrentFile(data)
	if err != nil {
		t.Fatalf("AddTorrentFile: %v", err)
	}

	if err := eng.SetFilePriority(id, 0, PrioritySkip); err != nil {
		t.Fatalf("SetFilePriority: %v", err)
	}

	files, err := eng.TorrentFiles(id)
	if err != nil {
		t.Fatalf("TorrentFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files: got %d, want 1", len(files))
	}
	if files[0].Priority != PrioritySkip {
		t.Errorf("priority: got %d, want %d", files[0].Priority, PrioritySkip)
	}
}

func TestSetFilePriorityMultiFile(t *testing.T) {
	eng := newTestEngine(t)

	data, _ := buildMultiFileTorrentData(t, "multi", 256, []struct {
		path []string
		size int
	}{
		{path: []string{"a.txt"}, size: 100},
		{path: []string{"b.txt"}, size: 200},
		{path: []string{"c.txt"}, size: 50},
	})
	id, err := eng.AddTorrentFile(data)
	if err != nil {
		t.Fatalf("AddTorrentFile: %v", err)
	}

	// Skip file index 1.
	if err := eng.SetFilePriority(id, 1, PrioritySkip); err != nil {
		t.Fatalf("SetFilePriority: %v", err)
	}

	files, _ := eng.TorrentFiles(id)
	if len(files) != 3 {
		t.Fatalf("files: got %d, want 3", len(files))
	}
	if files[0].Priority != PriorityNormal {
		t.Errorf("files[0].priority: got %d, want %d", files[0].Priority, PriorityNormal)
	}
	if files[1].Priority != PrioritySkip {
		t.Errorf("files[1].priority: got %d, want %d", files[1].Priority, PrioritySkip)
	}
	if files[2].Priority != PriorityNormal {
		t.Errorf("files[2].priority: got %d, want %d", files[2].Priority, PriorityNormal)
	}
}

func TestSetFilePriorityRejectsInvalidValue(t *testing.T) {
	eng := newTestEngine(t)
	data, _ := buildTestTorrentData(t, "bad.bin", 256, 256)
	id, _ := eng.AddTorrentFile(data)

	if err := eng.SetFilePriority(id, 0, 5); err == nil {
		t.Error("expected error for invalid priority value")
	}
}

func TestSetFilePriorityRejectsOutOfRangeIndex(t *testing.T) {
	eng := newTestEngine(t)
	data, _ := buildTestTorrentData(t, "oor.bin", 256, 256)
	id, _ := eng.AddTorrentFile(data)

	if err := eng.SetFilePriority(id, 99, PrioritySkip); err == nil {
		t.Error("expected error for out-of-range file_index")
	}
	if err := eng.SetFilePriority(id, -1, PrioritySkip); err == nil {
		t.Error("expected error for negative file_index")
	}
}

func TestSetFilePriorityUnknownTorrent(t *testing.T) {
	eng := newTestEngine(t)
	if err := eng.SetFilePriority("no-such-id", 0, PrioritySkip); err == nil {
		t.Error("expected error for unknown torrent")
	}
}

func TestSetFilePriorityPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		DownloadDir:     filepath.Join(dir, "downloads"),
		StatePath:       filepath.Join(dir, "state.json"),
		ListenPort:      0,
		MaxActive:       3,
		DisableDHT:      true,
		DisableListener: true,
	}

	eng1, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := eng1.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	data, _ := buildMultiFileTorrentData(t, "persist", 256, []struct {
		path []string
		size int
	}{
		{path: []string{"keep.txt"}, size: 100},
		{path: []string{"drop.txt"}, size: 200},
	})
	id, err := eng1.AddTorrentFile(data)
	if err != nil {
		t.Fatalf("AddTorrentFile: %v", err)
	}

	if err := eng1.SetFilePriority(id, 1, PrioritySkip); err != nil {
		t.Fatalf("SetFilePriority: %v", err)
	}
	if err := eng1.PauseTorrent(id); err != nil {
		t.Fatalf("PauseTorrent: %v", err)
	}
	if err := eng1.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Reopen — priority must be restored.
	eng2, err := New(cfg)
	if err != nil {
		t.Fatalf("New2: %v", err)
	}
	if err := eng2.Start(); err != nil {
		t.Fatalf("Start2: %v", err)
	}
	defer func() { _ = eng2.Stop() }()

	files, err := eng2.TorrentFiles(id)
	if err != nil {
		t.Fatalf("TorrentFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files: got %d, want 2", len(files))
	}
	if files[0].Priority != PriorityNormal {
		t.Errorf("files[0].priority: got %d, want %d", files[0].Priority, PriorityNormal)
	}
	if files[1].Priority != PrioritySkip {
		t.Errorf("files[1].priority: got %d, want %d (after restart)", files[1].Priority, PrioritySkip)
	}

	_ = os.RemoveAll(dir)
}
