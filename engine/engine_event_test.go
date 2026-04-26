package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/skmtkytr/stor/events"
)

// newEventTestEngine builds an Engine for event-emission tests. Unlike
// newTestEngine it does NOT register a t.Cleanup that calls Stop() — these
// tests must call Stop() explicitly so the bus quiesces before the recorder
// drain goroutine returns.
func newEventTestEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		DownloadDir:     filepath.Join(dir, "downloads"),
		StatePath:       filepath.Join(dir, "state.json"),
		ListenPort:      0,
		MaxActive:       3,
		DisableDHT:      true,
		DisableListener: true,
	}
	eng, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return eng
}

// subscribeRecorder spins up a recorder draining the engine bus. Returns the
// recorder plus a finish helper that stops the engine (which closes the bus
// and therefore our subscription channel), waits for the drain goroutine to
// exit, and returns the captured events.
//
// We deliberately stop the engine instead of cancelling the subscription
// context: cancelling races with concurrent Publish (the bus's Subscribe
// goroutine closes sub.C while another goroutine is mid-Publish). Stopping
// the engine quiesces all session goroutines first, so by the time
// Bus.Close() runs there are no in-flight publishes that could send on a
// closed channel.
func subscribeRecorder(t *testing.T, eng *Engine) (*events.Recorder, func() []events.Event) {
	t.Helper()
	ctx := context.Background() // never cancelled; engine.Stop() closes the bus.
	sub := eng.Bus().Subscribe(ctx, events.SubscribeOptions{Buffer: 256, Name: "test"})
	rec := events.NewRecorder()
	go rec.Drain(sub)
	return rec, func() []events.Event {
		_ = eng.Stop()
		rec.Wait()
		return rec.Events()
	}
}

// hasEvent reports whether evs contains at least one event whose Type matches.
// If torrentID is non-empty, it must also match.
func hasEvent(evs []events.Event, typ events.Type, torrentID string) bool {
	for _, ev := range evs {
		if ev.Type != typ {
			continue
		}
		if torrentID != "" && ev.TorrentID != torrentID {
			continue
		}
		return true
	}
	return false
}

// findStateChange searches for a TypeStateChanged event whose payload matches the
// given (from, to) pair. Empty arguments wildcard.
func findStateChange(evs []events.Event, from, to string, torrentID string) bool {
	for _, ev := range evs {
		if ev.Type != events.TypeStateChanged {
			continue
		}
		if torrentID != "" && ev.TorrentID != torrentID {
			continue
		}
		p, ok := ev.Payload.(events.StateChangedPayload)
		if !ok {
			continue
		}
		if from != "" && p.From != from {
			continue
		}
		if to != "" && p.To != to {
			continue
		}
		return true
	}
	return false
}

func writeTorrent(t *testing.T, name string, size int) string {
	t.Helper()
	data, _ := buildTestTorrentData(t, name, 256, size)
	path := filepath.Join(t.TempDir(), name+".torrent")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write torrent: %v", err)
	}
	return path
}

func TestEventTorrentAdded(t *testing.T) {
	eng := newEventTestEngine(t)
	rec, finish := subscribeRecorder(t, eng)
	_ = rec

	id, err := eng.AddTorrent(writeTorrent(t, "added", 256))
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}

	// Give the bus a moment to deliver.
	time.Sleep(50 * time.Millisecond)
	evs := finish()

	if !hasEvent(evs, events.TypeTorrentAdded, id) {
		t.Errorf("expected TypeTorrentAdded for %s, got events %v", id, eventTypes(evs))
	}

	// Verify payload shape
	for _, ev := range evs {
		if ev.Type != events.TypeTorrentAdded || ev.TorrentID != id {
			continue
		}
		p, ok := ev.Payload.(events.TorrentAddedPayload)
		if !ok {
			t.Fatalf("payload type: %T", ev.Payload)
		}
		if p.Name != "added" {
			t.Errorf("payload Name = %q, want %q", p.Name, "added")
		}
	}
}

// indexOf returns the index of the first event of type typ for torrentID,
// or -1 if no such event exists.
func indexOf(evs []events.Event, typ events.Type, torrentID string) int {
	for i, ev := range evs {
		if ev.Type != typ {
			continue
		}
		if torrentID != "" && ev.TorrentID != torrentID {
			continue
		}
		return i
	}
	return -1
}

// countOf returns the number of events matching typ + torrentID.
func countOf(evs []events.Event, typ events.Type, torrentID string) int {
	n := 0
	for _, ev := range evs {
		if ev.Type != typ {
			continue
		}
		if torrentID != "" && ev.TorrentID != torrentID {
			continue
		}
		n++
	}
	return n
}

// TestEventPauseRequestedFiresImmediately asserts that PauseTorrent emits
// TypeTorrentPauseRequested at API entry, before any state-change events.
// This is the "intent recorded" event — Deluge alert parity for the
// torrent_pause API call vs. the on_alert_torrent_paused alert.
func TestEventPauseRequestedFiresImmediately(t *testing.T) {
	eng := newEventTestEngine(t)
	rec, finish := subscribeRecorder(t, eng)
	_ = rec

	id, err := eng.AddTorrent(writeTorrent(t, "pause-req", 256))
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}

	// Let the session start and reach a state we can transition out of.
	time.Sleep(150 * time.Millisecond)

	if err := eng.PauseTorrent(id); err != nil {
		t.Fatalf("PauseTorrent: %v", err)
	}

	// Give events a moment to arrive.
	time.Sleep(100 * time.Millisecond)
	evs := finish()

	reqIdx := indexOf(evs, events.TypeTorrentPauseRequested, id)
	if reqIdx < 0 {
		t.Fatalf("expected TypeTorrentPauseRequested for %s, got %v", id, eventTypes(evs))
	}

	// Ordering: PauseRequested must come before the corresponding StateChanged
	// → paused (and before the confirmation TypeTorrentPaused). The session may
	// have already raced into StateError before pause, in which case the
	// state_changed → paused never happens; tolerate that.
	for i, ev := range evs {
		if ev.TorrentID != id {
			continue
		}
		if ev.Type == events.TypeStateChanged {
			p, ok := ev.Payload.(events.StateChangedPayload)
			if !ok {
				continue
			}
			if p.To == string(StatePaused) && i < reqIdx {
				t.Errorf("state_changed→paused at index %d preceded "+
					"PauseRequested at index %d; ordering broken",
					i, reqIdx)
			}
		}
		if ev.Type == events.TypeTorrentPaused && i < reqIdx {
			t.Errorf("TypeTorrentPaused at index %d preceded PauseRequested "+
				"at index %d; ordering broken",
				i, reqIdx)
		}
	}
}

// TestEventPausedFiresAfterStateConfirms asserts that the *confirmed*
// TypeTorrentPaused is emitted from setState() — i.e. after the state
// has actually transitioned to StatePaused. Order on the bus must be:
// PauseRequested → StateChanged(→ paused) → Paused.
func TestEventPausedFiresAfterStateConfirms(t *testing.T) {
	eng := newEventTestEngine(t)
	rec, finish := subscribeRecorder(t, eng)
	_ = rec

	id, err := eng.AddTorrent(writeTorrent(t, "pause-conf", 256))
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}

	time.Sleep(150 * time.Millisecond)
	if err := eng.PauseTorrent(id); err != nil {
		t.Fatalf("PauseTorrent: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	evs := finish()

	pausedIdx := indexOf(evs, events.TypeTorrentPaused, id)
	if pausedIdx < 0 {
		// If the session raced into StateError before pause could transition
		// to paused, no confirmation event fires — that's expected behavior
		// for setState which only emits on real (paused-bound) transitions.
		// The state had to be paused for the test to mean anything; skip.
		if findStateChange(evs, "", string(StateError), id) {
			t.Skip("session reached StateError before pause confirmation; " +
				"semantically correct, but no Paused event to verify")
		}
		t.Fatalf("expected TypeTorrentPaused for %s, got %v", id, eventTypes(evs))
	}

	// The matching StateChanged → paused must precede the confirmation.
	stateIdx := -1
	for i, ev := range evs {
		if ev.Type != events.TypeStateChanged || ev.TorrentID != id {
			continue
		}
		p, ok := ev.Payload.(events.StateChangedPayload)
		if !ok {
			continue
		}
		if p.To == string(StatePaused) {
			stateIdx = i
			break
		}
	}
	if stateIdx < 0 {
		t.Fatalf("expected state_changed→paused to precede TypeTorrentPaused, "+
			"got %v", evs)
	}
	if stateIdx >= pausedIdx {
		t.Errorf("state_changed→paused (idx %d) must precede TypeTorrentPaused "+
			"(idx %d); ordering broken", stateIdx, pausedIdx)
	}

	// And PauseRequested must precede StateChanged.
	reqIdx := indexOf(evs, events.TypeTorrentPauseRequested, id)
	if reqIdx < 0 || reqIdx >= stateIdx {
		t.Errorf("PauseRequested (idx %d) must precede state_changed→paused "+
			"(idx %d); ordering broken", reqIdx, stateIdx)
	}
}

// TestEventResumeRequestedFiresImmediately asserts ResumeTorrent emits
// TypeTorrentResumeRequested at API entry, before the session goroutine
// emits any state transitions for the resumed lifecycle.
func TestEventResumeRequestedFiresImmediately(t *testing.T) {
	eng := newEventTestEngine(t)
	rec, finish := subscribeRecorder(t, eng)
	_ = rec

	id, err := eng.AddTorrent(writeTorrent(t, "resume-req", 256))
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}

	time.Sleep(150 * time.Millisecond)
	if err := eng.PauseTorrent(id); err != nil {
		t.Fatalf("PauseTorrent: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := eng.ResumeTorrent(id); err != nil {
		t.Fatalf("ResumeTorrent: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	evs := finish()

	if !hasEvent(evs, events.TypeTorrentResumeRequested, id) {
		t.Errorf("expected TypeTorrentResumeRequested for %s, got %v",
			id, eventTypes(evs))
	}
}

// TestEventResumedFiresAfterStateConfirms asserts that the confirmed
// TypeTorrentResumed fires once the session has actually transitioned
// out of StatePaused (typically paused → adding, since ResumeTorrent
// re-queues through StateAdding). Order: ResumeRequested → StateChanged
// (paused → adding) → Resumed.
func TestEventResumedFiresAfterStateConfirms(t *testing.T) {
	eng := newEventTestEngine(t)
	_, finish := subscribeRecorder(t, eng)

	id, err := eng.AddTorrent(writeTorrent(t, "resume-conf", 256))
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}

	time.Sleep(150 * time.Millisecond)
	if err := eng.PauseTorrent(id); err != nil {
		t.Fatalf("PauseTorrent: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := eng.ResumeTorrent(id); err != nil {
		t.Fatalf("ResumeTorrent: %v", err)
	}
	// Allow the session goroutine to leave StatePaused.
	time.Sleep(200 * time.Millisecond)

	evs := finish()

	reqIdx := indexOf(evs, events.TypeTorrentResumeRequested, id)
	if reqIdx < 0 {
		t.Fatalf("missing TypeTorrentResumeRequested; got %v", eventTypes(evs))
	}

	resumedIdx := indexOf(evs, events.TypeTorrentResumed, id)
	if resumedIdx < 0 {
		t.Fatalf("missing TypeTorrentResumed (must fire on first transition "+
			"out of StatePaused); got %v", eventTypes(evs))
	}

	if reqIdx >= resumedIdx {
		t.Errorf("ResumeRequested (idx %d) must precede TypeTorrentResumed "+
			"(idx %d); ordering broken", reqIdx, resumedIdx)
	}

	// The first state_changed leaving paused must precede the Resumed
	// confirmation. We accept paused → any non-paused state.
	stateIdx := -1
	for i, ev := range evs {
		if ev.Type != events.TypeStateChanged || ev.TorrentID != id {
			continue
		}
		p, ok := ev.Payload.(events.StateChangedPayload)
		if !ok {
			continue
		}
		if p.From == string(StatePaused) && p.To != string(StatePaused) {
			stateIdx = i
			break
		}
	}
	if stateIdx < 0 || stateIdx >= resumedIdx {
		t.Errorf("state_changed leaving paused (idx %d) must precede "+
			"TypeTorrentResumed (idx %d); ordering broken",
			stateIdx, resumedIdx)
	}
}

// TestEventNoSpurious verifies that no-op state transitions (calling
// setState with the current state) emit nothing — neither StateChanged
// nor a Paused/Resumed confirmation. This protects against runaway
// duplicate events from any caller that re-asserts the current state.
func TestEventNoSpurious(t *testing.T) {
	eng := newEventTestEngine(t)
	rec, finish := subscribeRecorder(t, eng)
	_ = rec

	id, err := eng.AddTorrent(writeTorrent(t, "noop", 256))
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}

	// Drive the session to StatePaused first.
	time.Sleep(150 * time.Millisecond)
	if err := eng.PauseTorrent(id); err != nil {
		t.Fatalf("PauseTorrent: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Reach into the session and call setState(StatePaused) again. This
	// should be a no-op — the state is already paused.
	eng.mu.RLock()
	s := eng.sessions[id]
	eng.mu.RUnlock()
	if s == nil {
		t.Fatalf("session %s missing after pause", id)
	}

	// Snapshot event counts before the no-op call.
	preEvs := rec.Events()
	prePaused := countOf(preEvs, events.TypeTorrentPaused, id)
	preStateChanged := 0
	for _, ev := range preEvs {
		if ev.Type == events.TypeStateChanged && ev.TorrentID == id {
			preStateChanged++
		}
	}

	// Trigger the no-op transition.
	s.setState(StatePaused)
	time.Sleep(50 * time.Millisecond)

	evs := finish()

	postPaused := countOf(evs, events.TypeTorrentPaused, id)
	postStateChanged := 0
	for _, ev := range evs {
		if ev.Type == events.TypeStateChanged && ev.TorrentID == id {
			postStateChanged++
		}
	}

	if postPaused != prePaused {
		t.Errorf("no-op setState(StatePaused) emitted extra TypeTorrentPaused: "+
			"before=%d after=%d", prePaused, postPaused)
	}
	if postStateChanged != preStateChanged {
		t.Errorf("no-op setState(StatePaused) emitted extra TypeStateChanged: "+
			"before=%d after=%d", preStateChanged, postStateChanged)
	}
}

func TestEventRemoveEmitsRemoved(t *testing.T) {
	eng := newEventTestEngine(t)
	rec, finish := subscribeRecorder(t, eng)
	_ = rec

	id, err := eng.AddTorrent(writeTorrent(t, "rm", 256))
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if err := eng.RemoveTorrent(id, false); err != nil {
		t.Fatalf("RemoveTorrent: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	evs := finish()

	if !hasEvent(evs, events.TypeTorrentRemoved, id) {
		t.Errorf("expected TypeTorrentRemoved for %s, got %v", id, eventTypes(evs))
	}
	for _, ev := range evs {
		if ev.Type != events.TypeTorrentRemoved {
			continue
		}
		p, ok := ev.Payload.(events.TorrentRemovedPayload)
		if !ok {
			t.Fatalf("payload type: %T", ev.Payload)
		}
		if p.DeletedFiles != false {
			t.Errorf("DeletedFiles = %v, want false", p.DeletedFiles)
		}
	}
}

// eventTypes is a debug helper.
func eventTypes(evs []events.Event) []events.Type {
	out := make([]events.Type, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Type)
	}
	return out
}
