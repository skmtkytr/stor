package engine

import (
	"context"
	"crypto/sha1"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/skmtkytr/stor/download"
	"github.com/skmtkytr/stor/events"
	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/torrent"
)

// buildSingleFileTorrent constructs an in-memory single-file torrent
// (no tracker URLs, no announce-list — so the announcer goroutine started
// inside phaseDownload performs no network I/O). Returns the parsed
// TorrentFile, the file payload, and the SHA1 piece hashes.
func buildSingleFileTorrent(t *testing.T, name string, pieceLen int, dataLen int) (*torrent.TorrentFile, []byte) {
	t.Helper()
	payload := make([]byte, dataLen)
	for i := range payload {
		payload[i] = byte(i)
	}
	numPieces := (dataLen + pieceLen - 1) / pieceLen
	hashes := make([][20]byte, numPieces)
	for i := 0; i < numPieces; i++ {
		end := (i + 1) * pieceLen
		if end > dataLen {
			end = dataLen
		}
		hashes[i] = sha1.Sum(payload[i*pieceLen : end])
	}
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        name,
			PieceLength: int64(pieceLen),
			Length:      int64(dataLen),
			PieceHashes: hashes,
		},
	}
	return tf, payload
}

// runPhaseDownloadCompleted constructs a Session that already has all
// pieces (per the supplied bitfield placed in the record), drops the
// payload at savePath, and runs phaseDownload synchronously. With no work
// remaining, phaseDownload returns once it transitions to seeding (which
// fires StorageMoved if tmpDir was set). Returns the events captured on
// the bus.
func runPhaseDownloadCompleted(t *testing.T, tmpDir, downloadDir string) []events.Event {
	t.Helper()
	tf, payload := buildSingleFileTorrent(t, "complete.bin", 256, 1024)
	numPieces := len(tf.Info.PieceHashes)

	// Bitfield with every piece set -> DownloadWithParams sees Have ==
	// numPieces and returns immediately, so we never need real peers.
	bf := make(peer.Bitfield, (numPieces+7)/8)
	for i := 0; i < numPieces; i++ {
		bf.SetPiece(i)
	}

	if tmpDir != "" {
		if err := os.MkdirAll(tmpDir, 0o755); err != nil {
			t.Fatalf("mkdir tmp: %v", err)
		}
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatalf("mkdir dl: %v", err)
	}
	srcDir := tmpDir
	if srcDir == "" {
		srcDir = downloadDir
	}
	if err := os.WriteFile(filepath.Join(srcDir, tf.Info.Name), payload, 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	bus := events.New()
	t.Cleanup(bus.Close)

	rec := events.NewRecorder()
	sub := bus.Subscribe(context.Background(), events.SubscribeOptions{Buffer: 64, Name: "test"})
	go rec.Drain(sub)

	s := &Session{
		record: &TorrentRecord{
			ID:       "tid-1",
			Name:     tf.Info.Name,
			Bitfield: []byte(bf),
		},
		tf:          tf,
		downloadDir: downloadDir,
		tmpDir:      tmpDir,
		dlCfg:       download.DefaultDownloadConfig(),
		numWant:     1,
		bus:         bus,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.phaseDownload(ctx); err != nil {
		t.Fatalf("phaseDownload: %v", err)
	}

	// Allow event delivery to the recorder.
	time.Sleep(50 * time.Millisecond)
	bus.Close()
	rec.Wait()
	return rec.Events()
}

// TestEventStorageMoved verifies the rename from tmpDir → downloadDir on
// completion publishes a TypeStorageMoved with the correct From/To paths.
func TestEventStorageMoved(t *testing.T) {
	root := t.TempDir()
	tmpDir := filepath.Join(root, "tmp")
	dlDir := filepath.Join(root, "dl")

	evs := runPhaseDownloadCompleted(t, tmpDir, dlDir)

	var found *events.StorageMovedPayload
	for _, ev := range evs {
		if ev.Type != events.TypeStorageMoved {
			continue
		}
		p, ok := ev.Payload.(events.StorageMovedPayload)
		if !ok {
			t.Fatalf("payload type: %T", ev.Payload)
		}
		found = &p
	}
	if found == nil {
		t.Fatalf("expected TypeStorageMoved, got %v", eventTypes(evs))
	}
	wantFrom := filepath.Join(tmpDir, "complete.bin")
	wantTo := filepath.Join(dlDir, "complete.bin")
	if found.From != wantFrom || found.To != wantTo {
		t.Errorf("paths: from=%q to=%q, want from=%q to=%q", found.From, found.To, wantFrom, wantTo)
	}

	// Sanity: the file actually moved.
	if _, err := os.Stat(wantTo); err != nil {
		t.Errorf("file not moved to dl dir: %v", err)
	}
}

// TestEventStorageMovedNotFiredWithoutTmpDir verifies that when tmpDir is
// empty, no rename happens and no event is published.
func TestEventStorageMovedNotFiredWithoutTmpDir(t *testing.T) {
	root := t.TempDir()
	dlDir := filepath.Join(root, "dl")

	evs := runPhaseDownloadCompleted(t, "" /* tmpDir */, dlDir)

	for _, ev := range evs {
		if ev.Type == events.TypeStorageMoved {
			t.Fatalf("StorageMoved must not fire without tmpDir; got %v", ev)
		}
	}
}
