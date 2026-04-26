package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/skmtkytr/stor/download"
	"github.com/skmtkytr/stor/events"
)

// runPhaseDownloadWithRecordBitfield drives Session.phaseDownload with a
// caller-supplied byte slice as record.Bitfield. The phase is short-lived
// because we cancel ctx after a brief delay (we don't wait for a real
// download to complete; we just want to capture events emitted at the
// start of the phase, which includes the FastresumeRejected emission).
func runPhaseDownloadWithRecordBitfield(t *testing.T, recordBitfield []byte) []events.Event {
	t.Helper()
	tf, payload := buildSingleFileTorrent(t, "resume.bin", 256, 1024)

	root := t.TempDir()
	dlDir := filepath.Join(root, "dl")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Drop the file so VerifyPieces doesn't ENOENT on us.
	if err := os.WriteFile(filepath.Join(dlDir, tf.Info.Name), payload, 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	bus := events.New()
	t.Cleanup(bus.Close)

	rec := events.NewRecorder()
	sub := bus.Subscribe(context.Background(), events.SubscribeOptions{Buffer: 64, Name: "test"})
	go rec.Drain(sub)

	s := &Session{
		record: &TorrentRecord{
			ID:       "tid-fr",
			Name:     tf.Info.Name,
			Bitfield: recordBitfield,
		},
		tf:          tf,
		downloadDir: dlDir,
		dlCfg:       download.DefaultDownloadConfig(),
		numWant:     1,
		bus:         bus,
	}

	// Use a short timeout: when verify completes, the file is fully
	// present so DownloadWithParams returns nothing-to-do and we move on
	// quickly. If the persisted bitfield was the right size we still hit
	// the same fast path.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.phaseDownload(ctx); err != nil {
		// Normal completion path returns nil; any other error is unexpected.
		t.Fatalf("phaseDownload: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	bus.Close()
	rec.Wait()
	return rec.Events()
}

// TestEventFastresumeRejectedOnSizeMismatch verifies that a persisted
// bitfield whose length doesn't match the expected (numPieces+7)/8 byte
// count triggers exactly one TypeFastresumeRejected event with reason
// "bitfield size mismatch".
func TestEventFastresumeRejectedOnSizeMismatch(t *testing.T) {
	// numPieces == 4 -> expected bitfield size = 1 byte. Provide 7 bytes
	// to force the mismatch branch.
	bogus := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	evs := runPhaseDownloadWithRecordBitfield(t, bogus)

	var matches []events.FastresumeRejectedPayload
	for _, ev := range evs {
		if ev.Type != events.TypeFastresumeRejected {
			continue
		}
		p, ok := ev.Payload.(events.FastresumeRejectedPayload)
		if !ok {
			t.Fatalf("payload type: %T", ev.Payload)
		}
		matches = append(matches, p)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 FastresumeRejected, got %d (events=%v)", len(matches), eventTypes(evs))
	}
	if matches[0].Reason != "bitfield size mismatch" {
		t.Errorf("Reason=%q, want %q", matches[0].Reason, "bitfield size mismatch")
	}
	if matches[0].Detail == "" {
		t.Errorf("Detail empty, want size info")
	}
}

// TestEventNoFastresumeRejectedOnEmptyBitfield verifies the *initial*
// verify path (no persisted bitfield at all) does NOT publish the event:
// nothing was rejected, we simply have no fastresume artefact to begin
// with.
func TestEventNoFastresumeRejectedOnEmptyBitfield(t *testing.T) {
	evs := runPhaseDownloadWithRecordBitfield(t, nil)
	for _, ev := range evs {
		if ev.Type == events.TypeFastresumeRejected {
			t.Fatalf("FastresumeRejected must not fire on empty bitfield; got %v", ev)
		}
	}
}
