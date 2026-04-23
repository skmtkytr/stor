package engine

import (
	"sync"
	"testing"
	"time"

	"github.com/skmtkytr/stor/download"
	"github.com/skmtkytr/stor/torrent"
)

func TestSanitizeTorrentName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple name", "ubuntu-24.04.iso", false},
		{"name with spaces", "my file.txt", false},
		{"directory name", "My Torrent", false},
		{"dotdot traversal", "../../../etc/passwd", true},
		{"dotdot in middle", "foo/../../../etc/passwd", true},
		{"absolute path unix", "/etc/passwd", true},
		{"absolute path windows", "C:\\Windows\\system32", true},
		{"null byte", "foo\x00bar", true},
		{"empty name", "", true},
		{"just dots", "..", true},
		{"single dot", ".", true},
		{"backslash traversal", "foo\\..\\..\\etc\\passwd", true},
		{"encoded traversal", "foo/../../bar", true},
		{"trailing slash", "dirname/", false},
		{"subdirectory allowed", "artist/album", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sanitizeTorrentName(tt.input)
			if tt.wantErr && err == nil {
				t.Errorf("sanitizeTorrentName(%q) = nil, want error", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("sanitizeTorrentName(%q) = %v, want nil", tt.input, err)
			}
		})
	}
}

// TestSessionSnapCompleteReturns100 verifies that a completed torrent with no
// active progress or uploader (e.g. paused after seeding) reports 100% not 0%.
func TestSessionSnapCompleteReturns100(t *testing.T) {
	s := &Session{
		record: &TorrentRecord{
			State:      StateComplete,
			TotalBytes: 1024,
		},
	}
	snap := s.Snap()
	if snap.Percent != 100 {
		t.Errorf("Snap().Percent = %v, want 100 for StateComplete", snap.Percent)
	}
	if snap.Downloaded != 1024 {
		t.Errorf("Snap().Downloaded = %v, want 1024 for StateComplete", snap.Downloaded)
	}
}

// TestSessionSnapNonCompleteReturns0 verifies non-complete states without
// progress/uploader still return 0%.
func TestSessionSnapNonCompleteReturns0(t *testing.T) {
	for _, state := range []State{StatePaused, StateError, StateMetadata} {
		s := &Session{
			record: &TorrentRecord{
				State:      state,
				TotalBytes: 1024,
			},
		}
		snap := s.Snap()
		if snap.Percent != 0 {
			t.Errorf("state=%s: Snap().Percent = %v, want 0", state, snap.Percent)
		}
	}
}

func TestSessionSnapRace(t *testing.T) {
	// Verify that Snap() reads s.record.TotalBytes under the lock,
	// preventing data races with concurrent writers.
	s := &Session{
		record: &TorrentRecord{
			State:      StateSeeding,
			TotalBytes: 1000,
		},
	}
	// Simulate seeding without download progress (the code path that reads TotalBytes)
	up := download.NewUploader(nil, "", [20]byte{}, nil)
	s.uploader = up

	var wg sync.WaitGroup
	wg.Add(2)

	// Writer: continuously update TotalBytes
	go func() {
		defer wg.Done()
		for i := range 1000 {
			s.mu.Lock()
			s.record.TotalBytes = int64(i * 100)
			s.mu.Unlock()
		}
	}()

	// Reader: continuously call Snap()
	go func() {
		defer wg.Done()
		for range 1000 {
			snap := s.Snap()
			_ = snap.Total
		}
	}()

	wg.Wait()
}

func TestSessionTfAssignmentRace(t *testing.T) {
	// Verify that s.tf is written and read under lock protection.
	s := &Session{
		record: &TorrentRecord{
			State: StateMetadata,
		},
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Writer: simulate resolveMetadata setting s.tf
	go func() {
		defer wg.Done()
		for range 1000 {
			s.mu.Lock()
			s.tf = &torrent.TorrentFile{
				Info: torrent.Info{Name: "test"},
			}
			s.mu.Unlock()
		}
	}()

	// Reader: simulate other goroutine reading s.tf
	go func() {
		defer wg.Done()
		for range 1000 {
			s.mu.RLock()
			tf := s.tf
			if tf != nil {
				_ = tf.Info.Name
			}
			s.mu.RUnlock()
		}
	}()

	wg.Wait()
}

func TestEngineRejectsTraversalTorrentName(t *testing.T) {
	eng := newTestEngine(t)

	// Build a torrent with a malicious name containing path traversal
	data, _ := buildTestTorrentData(t, "../../../etc/evil", 256, 512)
	id, err := eng.AddTorrentFile(data)
	if err != nil {
		t.Fatalf("AddTorrentFile should succeed (validation happens on start): %v", err)
	}

	// Wait for the session to fail due to name validation
	time.Sleep(500 * time.Millisecond)

	info, err := eng.GetTorrent(id)
	if err != nil {
		t.Fatalf("GetTorrent: %v", err)
	}
	if info.State != StateError {
		t.Errorf("expected state %q for traversal name, got %q", StateError, info.State)
	}
}
