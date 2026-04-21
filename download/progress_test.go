package download

import (
	"sync/atomic"
	"testing"
)

// TestRateRegistryEMABasic exercises the same EMA math the old emaSpeed
// had, now driven through the shared rate registry via a deterministic
// tick. avg = avg*0.8 + sample*0.2 at a 1 Hz cadence.
func TestRateRegistryEMABasic(t *testing.T) {
	reg := newRateRegistry()
	var counter, rate atomic.Int64
	unreg := reg.register(&rateTarget{counter: &counter, rate: &rate})
	defer unreg()

	// Simulate: 1MB received in 1 second.
	counter.Add(1_000_000)
	reg.tickOnce(1000)

	// First tick: avg = 0*0.8 + 1000000*0.2 = 200000
	if got := rate.Load(); got != 200_000 {
		t.Errorf("after 1st tick: expected 200000, got %d", got)
	}

	// Second tick with same throughput.
	counter.Add(1_000_000)
	reg.tickOnce(1000)
	// avg = 200000*0.8 + 1000000*0.2 = 160000 + 200000 = 360000
	if got := rate.Load(); got != 360_000 {
		t.Errorf("after 2nd tick: expected 360000, got %d", got)
	}
}

func TestRateRegistryEMADecays(t *testing.T) {
	reg := newRateRegistry()
	var counter, rate atomic.Int64
	unreg := reg.register(&rateTarget{counter: &counter, rate: &rate})
	defer unreg()

	counter.Add(1_000_000)
	reg.tickOnce(1000) // avg = 200000

	// No data for several seconds — should decay.
	for range 10 {
		reg.tickOnce(1000) // avg *= 0.8 each time
	}

	if got := rate.Load(); got > 25_000 {
		t.Errorf("after 10 empty ticks, expected <25000, got %d", got)
	}
}

func TestRateRegistryEMAConverges(t *testing.T) {
	reg := newRateRegistry()
	var counter, rate atomic.Int64
	unreg := reg.register(&rateTarget{counter: &counter, rate: &rate})
	defer unreg()

	// Constant 500KB/s for 20 seconds — should converge near 500KB/s.
	for range 20 {
		counter.Add(500_000)
		reg.tickOnce(1000)
	}

	if got := rate.Load(); got < 490_000 || got > 510_000 {
		t.Errorf("after 20 ticks at 500KB/s, expected ~500000, got %d", got)
	}
}

func TestRateRegistryEMASubSecondTick(t *testing.T) {
	reg := newRateRegistry()
	var counter, rate atomic.Int64
	unreg := reg.register(&rateTarget{counter: &counter, rate: &rate})
	defer unreg()

	// 500KB in 500ms = 1MB/s rate.
	counter.Add(500_000)
	reg.tickOnce(500) // sample = 500000 * 1000 / 500 = 1000000

	// avg = 0*0.8 + 1000000*0.2 = 200000
	if got := rate.Load(); got != 200_000 {
		t.Errorf("sub-second tick: expected 200000, got %d", got)
	}
}

func TestProgressSnapSpeedAfterResume(t *testing.T) {
	p := NewProgress(100, 100*256*1024)
	defer p.Close()

	// Simulate resume: 50 pieces already done.
	p.SetInitial(50, 100*256*1024, 256*1024)

	// No new data added yet — speed should be 0.
	snap := p.Snap()
	if snap.DownSpeed != 0 {
		t.Errorf("expected 0 speed after resume with no new data, got %d", snap.DownSpeed)
	}

	// Add bytes and drive a deterministic tick instead of sleeping for
	// the real 1 Hz timer. Hold the tick lock so the background ticker
	// can't drain the counter between feed and tick.
	globalRateRegistry().withTickLock(func() {
		p.AddBytes(256 * 1024)
		globalRateRegistry().tickLocked(1000)
	})

	p.Add(256 * 1024)
	snap = p.Snap()
	if snap.DownSpeed == 0 {
		t.Error("expected non-zero speed after adding data and tick")
	}

	// Downloaded should include both initial + new.
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
