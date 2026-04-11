package download

import (
	"testing"
	"time"
)

func TestEMASpeedBasic(t *testing.T) {
	e := &emaSpeed{stop: make(chan struct{})}
	// Don't start the goroutine — drive ticks manually for deterministic tests.

	// Simulate: 1MB received in 1 second
	e.add(1_000_000)
	e.tick(1000)

	rate := e.rate()
	// First tick: avg = 0*0.8 + 1000000*0.2 = 200000
	if rate != 200_000 {
		t.Errorf("after 1st tick: expected 200000, got %d", rate)
	}

	// Second tick with same throughput
	e.add(1_000_000)
	e.tick(1000)
	// avg = 200000*0.8 + 1000000*0.2 = 160000 + 200000 = 360000
	rate = e.rate()
	if rate != 360_000 {
		t.Errorf("after 2nd tick: expected 360000, got %d", rate)
	}
}

func (e *emaSpeed) tick(elapsedMs int64) {
	e.mu.Lock()
	sample := float64(e.counter) * 1000.0 / float64(elapsedMs)
	e.average = e.average*0.8 + sample*0.2
	e.counter = 0
	e.mu.Unlock()
}

func TestEMASpeedDecays(t *testing.T) {
	e := &emaSpeed{stop: make(chan struct{})}

	e.add(1_000_000)
	e.tick(1000) // avg = 200000

	// No data for several seconds — should decay
	for range 10 {
		e.tick(1000) // avg *= 0.8 each time
	}

	rate := e.rate()
	if rate > 25_000 {
		t.Errorf("after 10 empty ticks, expected <25000, got %d", rate)
	}
}

func TestEMASpeedConverges(t *testing.T) {
	e := &emaSpeed{stop: make(chan struct{})}

	// Constant 500KB/s for 20 seconds — should converge near 500KB/s
	for range 20 {
		e.add(500_000)
		e.tick(1000)
	}

	rate := e.rate()
	if rate < 490_000 || rate > 510_000 {
		t.Errorf("after 20 ticks at 500KB/s, expected ~500000, got %d", rate)
	}
}

func TestEMASpeedSubSecondTick(t *testing.T) {
	e := &emaSpeed{stop: make(chan struct{})}

	// 500KB in 500ms = 1MB/s rate
	e.add(500_000)
	e.tick(500) // sample = 500000 * 1000 / 500 = 1000000

	rate := e.rate()
	// avg = 0*0.8 + 1000000*0.2 = 200000
	if rate != 200_000 {
		t.Errorf("sub-second tick: expected 200000, got %d", rate)
	}
}

func TestProgressSnapSpeedAfterResume(t *testing.T) {
	p := NewProgress(100, 100*256*1024)
	defer p.Close()

	// Simulate resume: 50 pieces already done
	p.SetInitial(50, 100*256*1024, 256*1024)

	// No new data added yet — speed should be 0
	snap := p.Snap()
	if snap.DownSpeed != 0 {
		t.Errorf("expected 0 speed after resume with no new data, got %d", snap.DownSpeed)
	}

	// Add bytes and wait for tick
	p.AddBytes(256 * 1024)
	time.Sleep(1200 * time.Millisecond) // wait for EMA tick

	p.Add(256 * 1024)
	snap = p.Snap()
	if snap.DownSpeed == 0 {
		t.Error("expected non-zero speed after adding data and tick")
	}

	// Downloaded should include both initial + new
	expectedDL := int64(51) * 256 * 1024
	if snap.Downloaded != expectedDL {
		t.Errorf("downloaded: got %d, want %d", snap.Downloaded, expectedDL)
	}
}

func TestProgressClose(t *testing.T) {
	p := NewProgress(10, 1000)
	p.Close()
	p.Close() // double close should not panic
}
