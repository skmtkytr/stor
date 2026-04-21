package download

import (
	"sync"
	"sync/atomic"
	"time"
)

// rateTarget is a single registration in the rateRegistry. It bundles a
// caller-owned counter (bytes accumulated since the last tick) with a
// caller-owned rate field (bytes/sec, written by the registry). The
// registry holds the EMA state internally — callers only interact via
// these two atomic fields.
type rateTarget struct {
	counter *atomic.Int64 // bytes since last tick (zeroed every tick)
	rate    *atomic.Int64 // current EMA, written by registry
}

// rateEntry couples a rateTarget with the registry-owned EMA state.
// The average field is touched only from the registry's ticker goroutine,
// so no additional synchronisation is required.
type rateEntry struct {
	target  *rateTarget
	average float64
}

// rateRegistry runs a single 1 Hz ticker goroutine that samples every
// registered counter, applies the libtorrent-style EMA
// (avg = avg*0.8 + sample*0.2), and publishes the result into the
// target's rate field.
//
// This replaces the per-Client / per-Progress goroutines that previously
// each ran their own 1 Hz ticker — on a torrent with hundreds of peers
// that meant hundreds of extra goroutines doing the same work.
//
// Registration is lazy (the goroutine starts on the first register()
// call) and the goroutine is daemon-wide: once started it runs for the
// lifetime of the process. Unregistering a target simply removes it
// from the iterated slice — the ticker keeps running.
type rateRegistry struct {
	mu      sync.Mutex
	entries []*rateEntry
	started bool
	// tickMu serialises tickOnce calls so the real-clock ticker goroutine
	// and any test-driven manual tick calls don't race on rateEntry.average.
	tickMu sync.Mutex
}

// globalRate is the process-wide rate registry. Lazily initialised by
// globalRateRegistry().
var (
	globalRateMu sync.Mutex
	globalRate   *rateRegistry
)

// globalRateRegistry returns the process-wide rate registry, creating it
// on first access.
func globalRateRegistry() *rateRegistry {
	globalRateMu.Lock()
	defer globalRateMu.Unlock()
	if globalRate == nil {
		globalRate = newRateRegistry()
	}
	return globalRate
}

// newRateRegistry constructs an empty, stopped registry. The ticker
// goroutine starts on the first register() call.
func newRateRegistry() *rateRegistry {
	return &rateRegistry{}
}

// register adds a target to the registry and returns a deregistration
// callback. The callback is idempotent and safe to call from any
// goroutine. The first register() call on a registry starts its
// background ticker goroutine.
func (r *rateRegistry) register(t *rateTarget) func() {
	entry := &rateEntry{target: t}
	r.mu.Lock()
	r.entries = append(r.entries, entry)
	if !r.started {
		r.started = true
		go r.run()
	}
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			for i, e := range r.entries {
				if e == entry {
					// order-preserving remove isn't required; use swap-delete
					last := len(r.entries) - 1
					r.entries[i] = r.entries[last]
					r.entries[last] = nil
					r.entries = r.entries[:last]
					break
				}
			}
			r.mu.Unlock()
		})
	}
}

// run is the single ticker goroutine. Started by the first register()
// call and never stopped — see rateRegistry docstring. All tick work
// goes through tickOnce, which serialises against test-driven manual
// ticks via tickMu.
func (r *rateRegistry) run() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := time.Now()
	for now := range ticker.C {
		elapsed := now.Sub(last).Milliseconds()
		last = now
		r.tickOnce(elapsed)
	}
}

// attachRateTargets registers a Client's down/up counters with the global
// rate registry and stores a combined deregistration callback on the
// client. Callers that construct a Client by hand (tests, incoming-upload
// handshake path, outgoing-peer handshake path) must call this before
// publishing the client to any ticker-visible state.
func attachRateTargets(c *Client) {
	reg := globalRateRegistry()
	unregDown := reg.register(&rateTarget{counter: &c.downCounter, rate: &c.downRate})
	unregUp := reg.register(&rateTarget{counter: &c.upCounter, rate: &c.upRate})
	c.unregRate = func() {
		unregDown()
		unregUp()
	}
}

// tickOnce performs one sampling pass over all registered targets.
// Exposed for tests so the EMA math can be driven deterministically
// without waiting for the real clock. Serialised against the background
// ticker via tickMu; test code that wants to feed counters and then
// tick atomically should use withTickLock instead.
func (r *rateRegistry) tickOnce(elapsedMs int64) {
	r.tickMu.Lock()
	defer r.tickMu.Unlock()
	r.tickLocked(elapsedMs)
}

// withTickLock runs fn while holding tickMu, blocking the background
// ticker. Test helper: lets a test feed counters and then call
// tickLocked atomically, so the background timer can't drain the
// counter between feed and tick.
func (r *rateRegistry) withTickLock(fn func()) {
	r.tickMu.Lock()
	defer r.tickMu.Unlock()
	fn()
}

// tickLocked does the sampling pass; caller must hold tickMu.
func (r *rateRegistry) tickLocked(elapsedMs int64) {
	if elapsedMs <= 0 {
		elapsedMs = 1000
	}
	r.mu.Lock()
	// Snapshot the slice so we don't hold the slice lock across the
	// atomic ops (registrations may arrive concurrently).
	entries := make([]*rateEntry, len(r.entries))
	copy(entries, r.entries)
	r.mu.Unlock()

	for _, e := range entries {
		bytes := e.target.counter.Swap(0)
		sample := float64(bytes) * 1000.0 / float64(elapsedMs)
		e.average = e.average*0.8 + sample*0.2
		e.target.rate.Store(int64(e.average))
	}
}
