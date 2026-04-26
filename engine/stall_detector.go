package engine

import (
	"context"
	"time"

	"github.com/skmtkytr/stor/events"
)

// Stall detection thresholds. Exported as constants so the test suite can
// reason about them without coupling to internal magic numbers.
const (
	// stallTickInterval is how often the detector polls the active peer count.
	stallTickInterval = 5 * time.Second
	// stallThreshold is the duration of continuous zero-peer state required
	// before TypeStalled fires.
	stallThreshold = 30 * time.Second
)

// peerCounter abstracts whatever supplies the live peer count. The download
// PeerManager satisfies this directly via PeerCount(); tests inject a fake.
type peerCounter interface {
	PeerCount() int
}

// stallDetector watches peer count and emits TypeStalled / TypeStallCleared
// events. Time and the polling tick are injectable so tests can drive the
// loop without real sleeps.
//
// Lifecycle: one detector per phaseDownload invocation. Run blocks until ctx
// is cancelled. A nil bus is tolerated (tests construct Sessions directly).
type stallDetector struct {
	bus       *events.Bus
	torrentID string
	peers     func() int // returns current peer count; 0 when unknown

	// now returns the current time. Defaults to time.Now; overridable for tests.
	now func() time.Time
	// tick is the polling channel. When nil, Run uses a real time.NewTicker
	// at stallTickInterval. Tests inject their own channel and step it
	// manually.
	tick <-chan time.Time
	// threshold overrides stallThreshold; 0 means use the default.
	threshold time.Duration
}

// run executes the detection loop until ctx is cancelled. It uses a real
// ticker when d.tick is nil. Safe to call exactly once per detector value.
func (d *stallDetector) run(ctx context.Context) {
	threshold := d.threshold
	if threshold <= 0 {
		threshold = stallThreshold
	}
	now := d.now
	if now == nil {
		now = time.Now
	}

	tick := d.tick
	if tick == nil {
		t := time.NewTicker(stallTickInterval)
		defer t.Stop()
		tick = t.C
	}

	var stalledSince time.Time
	var stalled bool

	for {
		select {
		case <-ctx.Done():
			return
		case tv, ok := <-tick:
			if !ok {
				return
			}
			// Prefer the timestamp delivered through the channel (tests
			// inject a fake clock that way). Real time.Ticker fills
			// tv.IsZero() == false with wall-clock time, which is what we
			// want anyway. If for any reason the value is zero, fall back
			// to the now() function.
			t := tv
			if t.IsZero() {
				t = now()
			}
			d.step(t, threshold, &stalledSince, &stalled)
		}
	}
}

// step encapsulates the per-tick logic so tests can drive a single iteration
// without juggling channels. Mutates *stalledSince / *stalled in place.
func (d *stallDetector) step(t time.Time, threshold time.Duration, stalledSince *time.Time, stalled *bool) {
	count := 0
	if d.peers != nil {
		count = d.peers()
	}
	if count <= 0 {
		if stalledSince.IsZero() {
			*stalledSince = t
		}
		if !*stalled && t.Sub(*stalledSince) >= threshold {
			*stalled = true
			d.publishStalled(t.Sub(*stalledSince))
		}
		return
	}
	// count > 0
	if *stalled {
		*stalled = false
		d.publishCleared(t.Sub(*stalledSince))
	}
	*stalledSince = time.Time{}
}

func (d *stallDetector) publishStalled(dur time.Duration) {
	if d.bus == nil {
		return
	}
	d.bus.Publish(events.Event{
		Type:      events.TypeStalled,
		TorrentID: d.torrentID,
		Payload: events.StalledPayload{
			DurationSeconds: int(dur.Seconds()),
			Reason:          "no_peers",
		},
	})
}

func (d *stallDetector) publishCleared(dur time.Duration) {
	if d.bus == nil {
		return
	}
	d.bus.Publish(events.Event{
		Type:      events.TypeStallCleared,
		TorrentID: d.torrentID,
		Payload: events.StallClearedPayload{
			StalledForSeconds: int(dur.Seconds()),
		},
	})
}
