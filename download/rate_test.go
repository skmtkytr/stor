package download

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// TestRateRegistryIsolatesTargets feeds two independent targets and verifies
// that after a tick, each target's rate field reflects only its own counter
// feed — no cross-contamination between registrations.
func TestRateRegistryIsolatesTargets(t *testing.T) {
	reg := newRateRegistry()

	var c1, r1 atomic.Int64
	var c2, r2 atomic.Int64

	t1 := &rateTarget{counter: &c1, rate: &r1}
	t2 := &rateTarget{counter: &c2, rate: &r2}

	unreg1 := reg.register(t1)
	unreg2 := reg.register(t2)
	defer unreg1()
	defer unreg2()

	// Feed distinct byte counts.
	c1.Add(1_000_000) // 1 MB/s sample
	c2.Add(500_000)   // 500 KB/s sample

	// Drive a deterministic tick (bypassing the real clock).
	reg.tickOnce(1000)

	// avg = 0*0.8 + sample*0.2
	if got := r1.Load(); got != 200_000 {
		t.Errorf("target1: want 200000, got %d", got)
	}
	if got := r2.Load(); got != 100_000 {
		t.Errorf("target2: want 100000, got %d", got)
	}

	// Counters must have been reset.
	if got := c1.Load(); got != 0 {
		t.Errorf("target1 counter not reset: got %d", got)
	}
	if got := c2.Load(); got != 0 {
		t.Errorf("target2 counter not reset: got %d", got)
	}

	// Second tick with same feeds should produce EMA-updated rates.
	c1.Add(1_000_000)
	c2.Add(500_000)
	reg.tickOnce(1000)

	// avg = 200000*0.8 + 1000000*0.2 = 360000
	if got := r1.Load(); got != 360_000 {
		t.Errorf("target1 2nd tick: want 360000, got %d", got)
	}
	// avg = 100000*0.8 + 500000*0.2 = 180000
	if got := r2.Load(); got != 180_000 {
		t.Errorf("target2 2nd tick: want 180000, got %d", got)
	}
}

// TestRateRegistryUnregister verifies a deregistered target no longer gets
// ticked, and its counter is not drained.
func TestRateRegistryUnregister(t *testing.T) {
	reg := newRateRegistry()

	var c atomic.Int64
	var r atomic.Int64
	t1 := &rateTarget{counter: &c, rate: &r}

	unreg := reg.register(t1)
	unreg()

	c.Add(1_000_000)
	reg.tickOnce(1000)

	if r.Load() != 0 {
		t.Errorf("unregistered target should not receive ticks, got rate %d", r.Load())
	}
	if c.Load() != 1_000_000 {
		t.Errorf("unregistered target counter should not be drained, got %d", c.Load())
	}
}

// TestClientsShareSingleTickerGoroutine creates many clients and asserts the
// live goroutine count does not grow linearly with client count — there is
// at most one shared rate-sampler goroutine plus some fixed overhead,
// regardless of how many Clients exist.
func TestClientsShareSingleTickerGoroutine(t *testing.T) {
	// Let any stray goroutines from other tests settle.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	const N = 10
	clients := make([]*Client, 0, N)
	for range N {
		c := &Client{}
		attachRateTargets(c)
		clients = append(clients, c)
	}

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()

	// With a shared ticker, creating 10 clients should add at most a very
	// small fixed number of goroutines (1 for the registry ticker, plus
	// scheduler/gc slack). The old design added 2 per client = 20.
	if delta := after - before; delta > N {
		t.Errorf("creating %d clients added %d goroutines; expected << 2*N=%d (shared ticker)",
			N, delta, 2*N)
	}

	// Clean up.
	for _, c := range clients {
		_ = c.Close()
	}
}

// TestClientEMAFedViaRegistry exercises the end-to-end path: byte feeds go
// into Client.downCounter/upCounter atomics, the registry ticker snaps and
// EMA-smooths, and Snapshot reads the resulting rate.
func TestClientEMAFedViaRegistry(t *testing.T) {
	c1 := &Client{Addr: "1.1.1.1:1"}
	attachRateTargets(c1)
	defer c1.Close()

	c2 := &Client{Addr: "2.2.2.2:2"}
	attachRateTargets(c2)
	defer c2.Close()

	// Feed each client's down counter with distinct byte totals, then
	// drive a deterministic tick while holding the tick lock so the
	// background ticker can't drain the counter between feed and tick.
	globalRateRegistry().withTickLock(func() {
		c1.downCounter.Add(2_000_000)
		c2.downCounter.Add(1_000_000)
		globalRateRegistry().tickLocked(1000)
	})

	snap1 := c1.Snapshot(0)
	snap2 := c2.Snapshot(0)

	if snap1.DownRate == 0 {
		t.Error("client1 DownRate should be non-zero after feed+tick")
	}
	if snap2.DownRate == 0 {
		t.Error("client2 DownRate should be non-zero after feed+tick")
	}
	if snap1.DownRate == snap2.DownRate {
		t.Errorf("clients should have distinct DownRates, both got %.0f", snap1.DownRate)
	}
	// avg = 0*0.8 + 2000000*0.2 = 400000
	if int64(snap1.DownRate) != 400_000 {
		t.Errorf("client1 DownRate: want 400000, got %.0f", snap1.DownRate)
	}
	if int64(snap2.DownRate) != 200_000 {
		t.Errorf("client2 DownRate: want 200000, got %.0f", snap2.DownRate)
	}
}
