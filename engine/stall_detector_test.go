package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skmtkytr/stor/events"
)

// startRecorder spins up an events.Recorder draining the given subscription.
// Call the returned `stop` to cancel the subscription, wait for the recorder
// goroutine to drain remaining buffered events, and return the captured slice.
func startRecorder(t *testing.T, bus *events.Bus, name string) (cancelSub context.CancelFunc, stop func() []events.Event) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sub := bus.Subscribe(ctx, events.SubscribeOptions{Buffer: 64, Name: name})
	rec := events.NewRecorder()
	go rec.Drain(sub)
	return cancel, func() []events.Event {
		cancel()
		rec.Wait()
		return rec.Events()
	}
}

// pumpTicks pushes n monotonically-increasing simulated timestamps onto ch,
// each interval apart from the previous, starting at base+interval.
func pumpTicks(ch chan<- time.Time, base time.Time, interval time.Duration, n int) time.Time {
	cur := base
	for i := 0; i < n; i++ {
		cur = cur.Add(interval)
		ch <- cur
	}
	return cur
}

// waitDrain busy-waits until len(ch) reaches 0 or the deadline elapses. The
// detector reads sequentially, so an empty buffer means every tick has been
// processed.
func waitDrain(ch <-chan time.Time, deadline time.Duration) {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if len(ch) == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// countByType returns how many events of typ are in evs.
func countByType(evs []events.Event, typ events.Type) int {
	n := 0
	for _, ev := range evs {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// TestStallDetectorEmitsAfterDuration drives the detector with a synthetic
// tick channel and a controllable clock. PeerCount stays at 0; after
// stallThreshold worth of simulated time, exactly one TypeStalled fires.
func TestStallDetectorEmitsAfterDuration(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	tick := make(chan time.Time, 16)
	d := &stallDetector{
		bus:       bus,
		torrentID: "tid-stalled",
		peers:     func() int { return 0 },
		tick:      tick,
		threshold: 30 * time.Second,
	}

	_, finish := startRecorder(t, bus, "stalled-test")

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	runDone := make(chan struct{})
	go func() { d.run(runCtx); close(runDone) }()

	// 7 ticks at 5s = 35s simulated. Threshold is 30s so the 7th tick fires.
	pumpTicks(tick, time.Unix(1_700_000_000, 0), 5*time.Second, 7)
	waitDrain(tick, 2*time.Second)

	runCancel()
	<-runDone
	evs := finish()

	if got := countByType(evs, events.TypeStalled); got != 1 {
		t.Fatalf("TypeStalled count = %d, want 1; events=%v", got, eventTypes(evs))
	}
	if got := countByType(evs, events.TypeStallCleared); got != 0 {
		t.Errorf("TypeStallCleared count = %d, want 0", got)
	}

	for _, ev := range evs {
		if ev.Type != events.TypeStalled {
			continue
		}
		if ev.TorrentID != "tid-stalled" {
			t.Errorf("TorrentID = %q, want tid-stalled", ev.TorrentID)
		}
		p, ok := ev.Payload.(events.StalledPayload)
		if !ok {
			t.Fatalf("payload type = %T, want StalledPayload", ev.Payload)
		}
		if p.Reason != "no_peers" {
			t.Errorf("Reason = %q, want no_peers", p.Reason)
		}
		if p.DurationSeconds < 30 {
			t.Errorf("DurationSeconds = %d, want >= 30", p.DurationSeconds)
		}
	}
}

// TestStallDetectorClears verifies that after a stall, recovery to nonzero
// peer count emits exactly one TypeStallCleared with a sensible duration.
func TestStallDetectorClears(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	tick := make(chan time.Time, 16)
	var peerCount atomic.Int32
	peerCount.Store(0)

	d := &stallDetector{
		bus:       bus,
		torrentID: "tid-clear",
		peers:     func() int { return int(peerCount.Load()) },
		tick:      tick,
		threshold: 30 * time.Second,
	}

	_, finish := startRecorder(t, bus, "clear-test")

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	runDone := make(chan struct{})
	go func() { d.run(runCtx); close(runDone) }()

	base := time.Unix(1_700_000_000, 0)
	cur := pumpTicks(tick, base, 5*time.Second, 7) // 35s elapsed → stall
	waitDrain(tick, 2*time.Second)

	peerCount.Store(3)
	tick <- cur.Add(5 * time.Second) // peers back → cleared
	waitDrain(tick, 2*time.Second)

	runCancel()
	<-runDone
	evs := finish()

	if got := countByType(evs, events.TypeStalled); got != 1 {
		t.Errorf("TypeStalled count = %d, want 1; events=%v", got, eventTypes(evs))
	}
	if got := countByType(evs, events.TypeStallCleared); got != 1 {
		t.Fatalf("TypeStallCleared count = %d, want 1; events=%v", got, eventTypes(evs))
	}

	for _, ev := range evs {
		if ev.Type != events.TypeStallCleared {
			continue
		}
		p, ok := ev.Payload.(events.StallClearedPayload)
		if !ok {
			t.Fatalf("payload type = %T, want StallClearedPayload", ev.Payload)
		}
		if p.StalledForSeconds < 30 {
			t.Errorf("StalledForSeconds = %d, want >= 30", p.StalledForSeconds)
		}
	}
}

// TestStallDetectorNoSpuriousEvents verifies no events fire while peers stay
// healthy. PeerCount > 0 across many ticks should produce nothing.
func TestStallDetectorNoSpuriousEvents(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	tick := make(chan time.Time, 32)
	d := &stallDetector{
		bus:       bus,
		torrentID: "tid-healthy",
		peers:     func() int { return 5 },
		tick:      tick,
		threshold: 30 * time.Second,
	}

	_, finish := startRecorder(t, bus, "healthy-test")

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	runDone := make(chan struct{})
	go func() { d.run(runCtx); close(runDone) }()

	pumpTicks(tick, time.Unix(1_700_000_000, 0), 5*time.Second, 12)
	waitDrain(tick, 2*time.Second)

	runCancel()
	<-runDone
	evs := finish()

	if len(evs) != 0 {
		t.Errorf("expected no events while peers healthy, got %v", eventTypes(evs))
	}
}

// TestStallDetectorOneShot verifies that once Stalled fires, continuing to
// have zero peers does NOT re-emit Stalled.
func TestStallDetectorOneShot(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	tick := make(chan time.Time, 32)
	d := &stallDetector{
		bus:       bus,
		torrentID: "tid-oneshot",
		peers:     func() int { return 0 },
		tick:      tick,
		threshold: 30 * time.Second,
	}

	_, finish := startRecorder(t, bus, "oneshot-test")

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	runDone := make(chan struct{})
	go func() { d.run(runCtx); close(runDone) }()

	// Twenty ticks (~100s) with peer count stuck at zero. Only one Stalled
	// event should ever fire.
	pumpTicks(tick, time.Unix(1_700_000_000, 0), 5*time.Second, 20)
	waitDrain(tick, 2*time.Second)

	runCancel()
	<-runDone
	evs := finish()

	if got := countByType(evs, events.TypeStalled); got != 1 {
		t.Fatalf("TypeStalled count = %d, want 1; events=%v", got, eventTypes(evs))
	}
	if got := countByType(evs, events.TypeStallCleared); got != 0 {
		t.Errorf("TypeStallCleared count = %d, want 0", got)
	}
}

// TestStallDetectorNilBusSafe verifies a nil bus does not panic the detector.
// session_test code constructs Sessions with no bus; the same defensive guard
// must hold here.
func TestStallDetectorNilBusSafe(t *testing.T) {
	tick := make(chan time.Time, 4)
	d := &stallDetector{
		bus:       nil,
		torrentID: "x",
		peers:     func() int { return 0 },
		tick:      tick,
		threshold: 30 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { d.run(ctx); close(done) }()

	tick <- time.Unix(60, 0) // step well past threshold; must not panic.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("detector did not exit after ctx cancel")
	}
}
