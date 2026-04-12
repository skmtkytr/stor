package download

import (
	"bufio"
	"crypto/sha1"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/torrent"
)

func TestUploaderNilBitfieldMeansHaveAll(t *testing.T) {
	// nil bitfield means "have all" (BEP 6 convention used in Client.HasPiece).
	// Uploader should treat nil bitfield the same way.
	data := make([]byte, 512)
	h0 := sha1.Sum(data[:256])
	h1 := sha1.Sum(data[256:])
	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name: "test", PieceLength: 256, Length: 512,
			PieceHashes: [][20]byte{h0, h1},
		},
	}
	tf.InfoHash = sha1.Sum([]byte("nil-bf-test"))

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test")
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// nil bitfield = have all
	u := NewUploader(tf, filePath, [20]byte{}, nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		hs, _ := peer.ReadHandshake(conn)
		u.HandleIncoming(conn, hs)
	}()

	conn, _ := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	defer func() { _ = conn.Close() }()

	_ = peer.WriteHandshake(conn, &peer.Handshake{
		InfoHash: tf.InfoHash, PeerID: [20]byte{9}, FastExtension: true,
	})
	_, _ = peer.ReadHandshake(conn)

	cr := bufio.NewReaderSize(conn, 64*1024)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	msg, err := peer.ReadMessage(cr)
	if err != nil {
		t.Fatalf("read init message: %v", err)
	}

	// With nil bitfield + FastExtension, should send HaveAll (MsgHaveAll=14)
	if msg.ID != peer.MsgHaveAll {
		t.Errorf("expected MsgHaveAll (14), got %d", msg.ID)
	}
}

func TestUploadRequestOverflowIndex(t *testing.T) {
	// A request with index=MaxUint32/2 and large pieceLen could cause
	// integer overflow in offset calculation. The uploader should reject it.
	data := make([]byte, 512)
	h0 := sha1.Sum(data[:256])
	h1 := sha1.Sum(data[256:])

	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name: "test", PieceLength: 256, Length: 512,
			PieceHashes: [][20]byte{h0, h1},
		},
	}
	tf.InfoHash = sha1.Sum([]byte("overflow-test"))

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test")
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	bf := make(peer.Bitfield, 1)
	bf.SetPiece(0)
	bf.SetPiece(1)

	u := NewUploader(tf, filePath, [20]byte{}, bf)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		hs, _ := peer.ReadHandshake(conn)
		u.HandleIncoming(conn, hs)
	}()

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	_ = peer.WriteHandshake(conn, &peer.Handshake{InfoHash: tf.InfoHash, PeerID: [20]byte{9}})
	_, _ = peer.ReadHandshake(conn)

	cr := bufio.NewReaderSize(conn, 64*1024)

	// Read bitfield
	msg, _ := peer.ReadMessage(cr)
	if msg == nil || msg.ID != peer.MsgBitfield {
		t.Fatal("expected bitfield")
	}

	// Send interested to get unchoked
	_ = (&peer.Message{ID: peer.MsgInterested}).Write(conn)

	// Read unchoke
	for {
		msg, err := peer.ReadMessage(cr)
		if err != nil {
			t.Fatal(err)
		}
		if msg != nil && msg.ID == peer.MsgUnchoke {
			break
		}
	}

	// Send request with overflowing index (2^31 * 256 > int64 max)
	req := peer.NewRequestMessage(0x7FFFFFFF, 0, 128)
	_ = req.Write(conn)

	// The server should disconnect (request out of bounds)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = peer.ReadMessage(cr)
	if err == nil {
		t.Fatal("expected connection to close after overflow request")
	}
}

func TestUploaderServePiece(t *testing.T) {
	// Create test data: 2 pieces of 256 bytes
	data := make([]byte, 512)
	for i := range data {
		data[i] = byte(i % 251)
	}
	h0 := sha1.Sum(data[:256])
	h1 := sha1.Sum(data[256:])

	tf := &torrent.TorrentFile{
		Info: torrent.Info{
			Name:        "test",
			PieceLength: 256,
			Length:      512,
			PieceHashes: [][20]byte{h0, h1},
		},
	}
	tf.InfoHash = sha1.Sum([]byte("test-info"))

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test")
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Create bitfield with all pieces
	bf := make(peer.Bitfield, 1)
	bf.SetPiece(0)
	bf.SetPiece(1)

	var peerID [20]byte
	copy(peerID[:], "test-uploader-peerid")

	u := NewUploader(tf, filePath, peerID, bf)

	// Start TCP listener
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	// Accept one connection and hand to uploader
	accepted := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Read remote handshake
		hs, err := peer.ReadHandshake(conn)
		if err != nil {
			_ = conn.Close()
			return
		}
		close(accepted)
		u.HandleIncoming(conn, hs)
	}()

	// Client connects
	clientConn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientConn.Close() }()

	// Client sends handshake
	clientHS := &peer.Handshake{InfoHash: tf.InfoHash, PeerID: [20]byte{9, 9, 9}}
	if err := peer.WriteHandshake(clientConn, clientHS); err != nil {
		t.Fatal(err)
	}

	// Wait for server to accept
	<-accepted

	// Read server's handshake response
	hs, err := peer.ReadHandshake(clientConn)
	if err != nil {
		t.Fatalf("read handshake: %v", err)
	}
	if hs.InfoHash != tf.InfoHash {
		t.Fatal("info hash mismatch")
	}

	cr := bufio.NewReaderSize(clientConn, 64*1024)

	// Read bitfield
	msg, err := peer.ReadMessage(cr)
	if err != nil {
		t.Fatalf("read bitfield: %v", err)
	}
	if msg.ID != peer.MsgBitfield {
		t.Fatalf("expected bitfield, got %d", msg.ID)
	}

	// Force unchoke the client (normally PeerManager does this on a timer)
	time.Sleep(50 * time.Millisecond)
	u.mu.Lock()
	for _, c := range u.clients {
		c.choking = false // direct field set to avoid writing to conn
	}
	u.mu.Unlock()

	// Send interested + request
	interested := &peer.Message{ID: peer.MsgInterested}
	if err := interested.Write(clientConn); err != nil {
		t.Fatal(err)
	}
	req := peer.NewRequestMessage(0, 0, 256)
	if err := req.Write(clientConn); err != nil {
		t.Fatal(err)
	}

	// Read piece response
	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	msg, err = peer.ReadMessage(cr)
	if err != nil {
		t.Fatalf("read piece: %v", err)
	}
	if msg.ID != peer.MsgPiece {
		t.Fatalf("expected piece, got %d", msg.ID)
	}

	idx, begin, block, err := peer.ParsePiece(msg.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if idx != 0 || begin != 0 || len(block) != 256 {
		t.Fatalf("unexpected piece: idx=%d begin=%d len=%d", idx, begin, len(block))
	}

	for i := range 256 {
		if block[i] != data[i] {
			t.Fatalf("data mismatch at byte %d", i)
		}
	}
}
