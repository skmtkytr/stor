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

func TestEventPauseEmitsPausedAndStateChanged(t *testing.T) {
	eng := newEventTestEngine(t)
	rec, finish := subscribeRecorder(t, eng)
	_ = rec

	id, err := eng.AddTorrent(writeTorrent(t, "pause", 256))
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

	if !hasEvent(evs, events.TypeTorrentPaused, id) {
		t.Errorf("expected TypeTorrentPaused for %s, got %v", id, eventTypes(evs))
	}
	// The session may have raced into StateError (no real tracker) before pause.
	// Accept a transition into either paused or error.
	if !findStateChange(evs, "", string(StatePaused), id) &&
		!findStateChange(evs, "", string(StateError), id) {
		t.Errorf("expected a state_changed to paused or error for %s, got %v", id, evs)
	}
}

func TestEventResumeEmitsResumed(t *testing.T) {
	eng := newEventTestEngine(t)
	rec, finish := subscribeRecorder(t, eng)
	_ = rec

	id, err := eng.AddTorrent(writeTorrent(t, "resume", 256))
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

	if !hasEvent(evs, events.TypeTorrentResumed, id) {
		t.Errorf("expected TypeTorrentResumed for %s, got %v", id, eventTypes(evs))
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
