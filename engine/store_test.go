package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := NewStore(path)

	s.Put(&TorrentRecord{
		ID:         "abc123",
		Name:       "test-file.txt",
		Source:     "magnet:?xt=urn:btih:abc123",
		State:      StateDownloading,
		TotalBytes: 1024,
		AddedAt:    1700000000,
	})
	s.Put(&TorrentRecord{
		ID:    "def456",
		Name:  "other.zip",
		State: StateComplete,
	})

	if s.Len() != 2 {
		t.Fatalf("len: got %d, want 2", s.Len())
	}

	// Save
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Load into new store
	s2 := NewStore(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	if s2.Len() != 2 {
		t.Fatalf("loaded len: got %d, want 2", s2.Len())
	}

	r, ok := s2.Get("abc123")
	if !ok {
		t.Fatal("abc123 not found")
	}
	if r.Name != "test-file.txt" {
		t.Errorf("name: got %q", r.Name)
	}
	if r.State != StateDownloading {
		t.Errorf("state: got %q", r.State)
	}
	if r.TotalBytes != 1024 {
		t.Errorf("total_bytes: got %d", r.TotalBytes)
	}
}

func TestStoreDelete(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "state.json"))

	s.Put(&TorrentRecord{ID: "a", Name: "a"})
	s.Put(&TorrentRecord{ID: "b", Name: "b"})
	s.Delete("a")

	if s.Len() != 1 {
		t.Fatalf("len after delete: got %d, want 1", s.Len())
	}
	if _, ok := s.Get("a"); ok {
		t.Error("a should be deleted")
	}
}

func TestStoreLoadNonExistent(t *testing.T) {
	s := NewStore("/nonexistent/path/state.json")
	if err := s.Load(); err != nil {
		t.Fatalf("load nonexistent should not error: %v", err)
	}
	if s.Len() != 0 {
		t.Error("should be empty")
	}
}

func TestStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := NewStore(path)

	s.Put(&TorrentRecord{ID: "x", Name: "x"})
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Temp file should not exist
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file should be cleaned up after rename")
	}

	// Main file should exist
	if _, err := os.Stat(path); err != nil {
		t.Errorf("state file should exist: %v", err)
	}
}

func TestStoreSaveConsistentSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := NewStore(path)

	s.Put(&TorrentRecord{ID: "snap", Name: "original", State: StateDownloading})

	// Modify the record concurrently during Save
	// Save should capture a snapshot, not be affected by later mutations
	done := make(chan error)
	go func() {
		done <- s.Save()
	}()

	// Mutate record while Save may be running
	r, _ := s.Get("snap")
	r.Name = "mutated"
	r.State = StateComplete

	if err := <-done; err != nil {
		t.Fatalf("save: %v", err)
	}

	// Load and verify the saved state is consistent
	s2 := NewStore(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	r2, _ := s2.Get("snap")

	// The name should be either "original" or "mutated" but not a mix
	// With deep copy fix, it should always be "original"
	if r2.Name != "original" {
		t.Errorf("expected snapshot to preserve 'original', got %q", r2.Name)
	}
}

func TestStoreAll(t *testing.T) {
	s := NewStore("")
	s.Put(&TorrentRecord{ID: "1"})
	s.Put(&TorrentRecord{ID: "2"})
	s.Put(&TorrentRecord{ID: "3"})

	all := s.All()
	if len(all) != 3 {
		t.Errorf("all: got %d, want 3", len(all))
	}
}

func TestStoreBitfieldPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := NewStore(path)

	bf := []byte{0b10101010, 0b01010101}
	s.Put(&TorrentRecord{
		ID:       "bf-test",
		Bitfield: bf,
	})

	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	s2 := NewStore(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	r, ok := s2.Get("bf-test")
	if !ok {
		t.Fatal("not found")
	}
	if len(r.Bitfield) != 2 || r.Bitfield[0] != 0b10101010 || r.Bitfield[1] != 0b01010101 {
		t.Errorf("bitfield: got %v", r.Bitfield)
	}
}

func TestStoreKnownPeersPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := NewStore(path)

	// Non-zero value must survive Save→Load.
	s.Put(&TorrentRecord{ID: "kp-test", KnownPeers: 57})
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	s2 := NewStore(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	r, ok := s2.Get("kp-test")
	if !ok {
		t.Fatal("kp-test not found after load")
	}
	if r.KnownPeers != 57 {
		t.Errorf("KnownPeers: got %d, want 57", r.KnownPeers)
	}

	// Zero value: omitempty omits it from JSON; loading must still give 0.
	s.Put(&TorrentRecord{ID: "kp-zero", KnownPeers: 0})
	if err := s.Save(); err != nil {
		t.Fatalf("save zero: %v", err)
	}
	s3 := NewStore(path)
	if err := s3.Load(); err != nil {
		t.Fatalf("load zero: %v", err)
	}
	r2, ok := s3.Get("kp-zero")
	if !ok {
		t.Fatal("kp-zero not found after load")
	}
	if r2.KnownPeers != 0 {
		t.Errorf("KnownPeers zero: got %d, want 0", r2.KnownPeers)
	}
}
