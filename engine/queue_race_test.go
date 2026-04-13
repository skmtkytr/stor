package engine

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestQueueBottomRace verifies that concurrent QueueBottom calls
// don't produce inconsistent queue positions (TOCTOU race).
// Run with -race.
func TestQueueBottomRace(t *testing.T) {
	eng := newTestEngine(t)

	var ids []string
	for i := range 5 {
		data, _ := buildTestTorrentData(t, "race"+string(rune('A'+i))+".txt", 256, 256)
		path := filepath.Join(t.TempDir(), "race"+string(rune('A'+i))+".torrent")
		_ = os.WriteFile(path, data, 0o644)
		id, _ := eng.AddTorrent(path)
		ids = append(ids, id)
		time.Sleep(10 * time.Millisecond)
	}

	// Pause all
	for _, id := range ids {
		_ = eng.PauseTorrent(id)
	}
	time.Sleep(50 * time.Millisecond)

	// Concurrent QueueBottom calls
	var wg sync.WaitGroup
	for _, id := range ids[:3] {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = eng.QueueBottom(id)
		}(id)
	}
	wg.Wait()

	// Verify: all positions must be unique and in [0, len(ids))
	positions := make(map[int]string)
	for _, id := range ids {
		info, _ := eng.GetTorrent(id)
		if _, dup := positions[info.QueuePosition]; dup {
			t.Errorf("duplicate queue position %d: %s and %s", info.QueuePosition, positions[info.QueuePosition], id)
		}
		positions[info.QueuePosition] = id
		if info.QueuePosition < 0 || info.QueuePosition >= len(ids) {
			t.Errorf("queue position out of range: %d for %s", info.QueuePosition, id)
		}
	}
}
