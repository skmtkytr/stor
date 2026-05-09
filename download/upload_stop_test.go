package download

import (
	"crypto/sha1"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/torrent"
)

// TestUploaderStopReleasesServeGoroutines ensures Stop blocks until every
// in-flight HandleIncoming goroutine has fully exited. Without this guarantee
// a subsequent rename of the source tree on an SMB/CIFS mount fails with a
// sharing violation because the deferred file Close in serveLoop hasn't fired.
func TestUploaderStopReleasesServeGoroutines(t *testing.T) {
	data := make([]byte, 512)
	h0 := sha1.Sum(data[:256])
	h1 := sha1.Sum(data[256:])
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name: "test", PieceLength: 256, Length: 512,
			PieceHashes: [][20]byte{h0, h1},
		},
	}
	tf.InfoHash = sha1.Sum([]byte("stop-test"))

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test")
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	u := NewUploader(tf, filePath, [20]byte{}, nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	var serveExited atomic.Bool
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		hs, err := peer.ReadHandshake(conn)
		if err != nil {
			_ = conn.Close()
			return
		}
		u.HandleIncoming(conn, hs)
		serveExited.Store(true)
	}()

	clientConn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientConn.Close() }()

	if err := peer.WriteHandshake(clientConn, &peer.Handshake{
		InfoHash: tf.InfoHash, PeerID: [20]byte{9}, FastExtension: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.ReadHandshake(clientConn); err != nil {
		t.Fatal(err)
	}

	// Wait until the serve goroutine has registered the client (so Stop has
	// something to close).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		u.mu.Lock()
		n := len(u.clients)
		u.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	done := make(chan struct{})
	go func() {
		u.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not return within 3s")
	}

	if !serveExited.Load() {
		t.Fatal("HandleIncoming goroutine did not exit before Stop returned")
	}
}

// TestUploaderStopRefusesNewConnections ensures HandleIncoming called after
// Stop closes the conn instead of leaking a goroutine that would hold a fd.
func TestUploaderStopRefusesNewConnections(t *testing.T) {
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name: "x", PieceLength: 256, Length: 256,
			PieceHashes: [][20]byte{{}},
		},
	}
	tf.InfoHash = sha1.Sum([]byte("stop-refuse"))

	u := NewUploader(tf, "/nonexistent", [20]byte{}, nil)
	u.Stop()

	a, b := net.Pipe()
	defer func() { _ = a.Close() }()

	hs := &peer.Handshake{InfoHash: tf.InfoHash, PeerID: [20]byte{1}}

	gone := make(chan struct{})
	go func() {
		u.HandleIncoming(b, hs)
		close(gone)
	}()

	select {
	case <-gone:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleIncoming did not return after Stop")
	}

	// b should be closed; reading from a returns EOF.
	_ = a.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	if _, err := a.Read(buf); err == nil {
		t.Fatal("expected pipe to be closed after refused HandleIncoming")
	}
}

// TestUploaderStopIsIdempotent ensures repeat Stop calls are safe.
func TestUploaderStopIsIdempotent(t *testing.T) {
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name: "x", PieceLength: 256, Length: 256,
			PieceHashes: [][20]byte{{}},
		},
	}
	u := NewUploader(tf, "/nonexistent", [20]byte{}, nil)
	u.Stop()
	u.Stop()
}
