package download

import (
	"testing"
	"time"
)

func TestSpeedWindowBasic(t *testing.T) {
	var sw speedWindow
	sw.add(1000)
	// Within the same second, speed should reflect the data
	speed := sw.bytesPerSec()
	if speed != 1000 {
		t.Errorf("expected 1000 B/s, got %d", speed)
	}
}

func TestSpeedWindowExcludesOldBuckets(t *testing.T) {
	var sw speedWindow
	// Simulate data in old bucket
	sw.mu.Lock()
	sw.buckets[0] = 99999
	sw.times[0] = time.Now().Unix() - int64(speedWindowSize) - 1 // too old
	sw.mu.Unlock()

	// Add new data
	sw.add(500)
	speed := sw.bytesPerSec()
	// Should only reflect new data, not the stale bucket
	if speed != 500 {
		t.Errorf("expected 500 B/s, got %d", speed)
	}
}

func TestProgressSnapSpeedAfterResume(t *testing.T) {
	p := NewProgress(100, 100*256*1024)

	// Simulate resume: 50 pieces already done
	p.SetInitial(50, 100*256*1024, 256*1024)

	// No new data added yet — speed should be 0
	snap := p.Snap()
	if snap.DownSpeed != 0 {
		t.Errorf("expected 0 speed after resume with no new data, got %d", snap.DownSpeed)
	}

	// Add new data (AddBytes tracks speed, Add tracks piece count)
	p.AddBytes(256 * 1024)
	p.Add(256 * 1024)
	snap = p.Snap()
	if snap.DownSpeed == 0 {
		t.Error("expected non-zero speed after adding data")
	}

	// Downloaded should include both initial + new
	expectedDL := int64(51) * 256 * 1024
	if snap.Downloaded != expectedDL {
		t.Errorf("downloaded: got %d, want %d", snap.Downloaded, expectedDL)
	}
}
