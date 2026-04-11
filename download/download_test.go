package download

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/tracker"
)

// fakePeer creates a listener that acts as a minimal BitTorrent peer.
// It responds to handshake, sends bitfield + unchoke, and serves piece data.
func fakePeer(t *testing.T, infoHash [20]byte, pieces map[int][]byte) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Read handshake
		hs, err := peer.ReadHandshake(conn)
		if err != nil {
			return
		}
		if hs.InfoHash != infoHash {
			return
		}

		// Write handshake
		_ = peer.WriteHandshake(conn, &peer.Handshake{
			InfoHash: infoHash,
			PeerID:   [20]byte{'-', 'F', 'K', '0', '0', '0', '1', '-'},
		})

		// Send bitfield (all pieces available)
		numPieces := 0
		for k := range pieces {
			if k+1 > numPieces {
				numPieces = k + 1
			}
		}
		bfLen := (numPieces + 7) / 8
		bf := make([]byte, bfLen)
		for k := range pieces {
			bf[k/8] |= 1 << (7 - k%8)
		}
		bfMsg := &peer.Message{ID: peer.MsgBitfield, Payload: bf}
		_ = bfMsg.Write(conn)

		// Read messages and respond
		for {
			msg, err := peer.ReadMessage(conn)
			if err != nil {
				return
			}
			if msg == nil {
				continue
			}

			switch msg.ID {
			case peer.MsgInterested:
				// Send unchoke
				unchoke := &peer.Message{ID: peer.MsgUnchoke}
				_ = unchoke.Write(conn)
			case peer.MsgRequest:
				if len(msg.Payload) != 12 {
					return
				}
				idx := binary.BigEndian.Uint32(msg.Payload[0:4])
				begin := binary.BigEndian.Uint32(msg.Payload[4:8])
				length := binary.BigEndian.Uint32(msg.Payload[8:12])

				data, ok := pieces[int(idx)]
				if !ok {
					return
				}

				end := int(begin) + int(length)
				if end > len(data) {
					end = len(data)
				}
				block := data[begin:end]

				// Small delay to let upload exchange happen during download
				time.Sleep(10 * time.Millisecond)

				payload := make([]byte, 8+len(block))
				binary.BigEndian.PutUint32(payload[0:4], idx)
				binary.BigEndian.PutUint32(payload[4:8], begin)
				copy(payload[8:], block)

				pieceMsg := &peer.Message{ID: peer.MsgPiece, Payload: payload}
				_ = pieceMsg.Write(conn)
			}
		}
	}()

	return ln
}

func TestDownloadPiece(t *testing.T) {
	infoHash := [20]byte{0xAA}
	peerID := [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'}

	// Create a piece of data
	pieceData := make([]byte, BlockSize+100) // slightly larger than one block
	for i := range pieceData {
		pieceData[i] = byte(i % 256)
	}
	pieceHash := sha1.Sum(pieceData)

	pieces := map[int][]byte{0: pieceData}
	ln := fakePeer(t, infoHash, pieces)
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().(*net.TCPAddr)
	p := tracker.Peer{IP: net.IPv4(127, 0, 0, 1), Port: uint16(addr.Port)}

	client, err := NewClient(p, infoHash, peerID)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	if !client.HasPiece(0) {
		t.Fatal("client should report having piece 0")
	}

	pw := PieceWork{Index: 0, Hash: pieceHash, Length: len(pieceData)}
	data, release, err := client.DownloadPiece(pw)
	if err != nil {
		t.Fatalf("DownloadPiece failed: %v", err)
	}
	if release != nil {
		defer release()
	}

	if len(data) != len(pieceData) {
		t.Errorf("data length: got %d, want %d", len(data), len(pieceData))
	}
	for i := range data {
		if data[i] != pieceData[i] {
			t.Errorf("data mismatch at byte %d: got %d, want %d", i, data[i], pieceData[i])
			break
		}
	}
}

// TestUploadWhileDownloading verifies the full upload flow:
// 1. We connect to a fake peer that has pieces we want
// 2. We send our bitfield showing pieces we already have
// 3. The fake peer sends MsgInterested + waits for our MsgUnchoke
// 4. The fake peer sends MsgRequest for a piece we have
// 5. We respond with MsgPiece containing the actual data
func TestUploadWhileDownloading(t *testing.T) {
	infoHash := [20]byte{0xCC}
	ourPeerID := [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'}

	// Piece 0: 3 blocks (>BlockSize) so DownloadPiece runs multiple loop
	// iterations, giving time for the upload exchange to happen mid-download.
	// Piece 1: data WE have (and should upload to the fake peer)
	piece0Data := make([]byte, BlockSize*3)
	for i := range piece0Data {
		piece0Data[i] = byte(i % 256)
	}
	piece1Data := make([]byte, 128)
	for i := range piece1Data {
		piece1Data[i] = byte(i + 100)
	}

	piece0Hash := sha1.Sum(piece0Data)
	_ = sha1.Sum(piece1Data) // piece1 is uploaded, not downloaded — hash not needed here

	// Channels to signal test progress
	gotUnchoke := make(chan bool, 1)
	gotPieceData := make(chan []byte, 1)

	// Create a fake peer that:
	// - Has piece 0 (will upload to us)
	// - Does NOT have piece 1
	// - After handshake, sends bitfield, unchoke
	// - Then sends MsgInterested (wants our piece 1)
	// - After receiving our MsgUnchoke, sends MsgRequest for piece 1
	// - Verifies it receives MsgPiece with correct data
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
		defer func() { _ = conn.Close() }()
		// Disable Nagle to ensure small messages are sent immediately
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
		}

		// Handshake
		hs, _ := peer.ReadHandshake(conn)
		if hs.InfoHash != infoHash {
			return
		}
		_ = peer.WriteHandshake(conn, &peer.Handshake{
			InfoHash: infoHash,
			PeerID:   [20]byte{'-', 'F', 'K', '0', '0', '0', '2', '-'},
		})

		// Send bitfield: we have piece 0 only
		bf := make([]byte, 1)
		bf[0] = 0x80 // piece 0 set
		bfMsg := &peer.Message{ID: peer.MsgBitfield, Payload: bf}
		_ = bfMsg.Write(conn)

		// Send unchoke (so they can download from us)
		_ = (&peer.Message{ID: peer.MsgUnchoke}).Write(conn)

		// Read messages - expect their bitfield, interested, requests, etc.
		// Also: send MsgInterested to request their pieces
		sentInterested := false
		sentRequest := false

		for {
			msg, err := peer.ReadMessage(conn)
			if err != nil {
				return
			}
			if msg == nil {
				continue
			}

			switch msg.ID {
			case peer.MsgBitfield:
				// They sent their bitfield - check if piece 1 is set
				theirBF := peer.Bitfield(msg.Payload)
				if theirBF.HasPiece(1) && !sentInterested {
					// Send MsgInterested
					_ = (&peer.Message{ID: peer.MsgInterested}).Write(conn)
					sentInterested = true
				}

			case peer.MsgHave:
				// They completed a piece
				idx, _ := peer.ParseHave(msg.Payload)
				if int(idx) == 1 && !sentInterested {
					_ = (&peer.Message{ID: peer.MsgInterested}).Write(conn)
					sentInterested = true
				}

			case peer.MsgUnchoke:
				// They unchoked us! Now we can request piece 1
				gotUnchoke <- true
				if !sentRequest {
					req := peer.NewRequestMessage(1, 0, uint32(len(piece1Data)))
					_ = req.Write(conn)
					sentRequest = true
				}

			case peer.MsgPiece:
				// They sent us a piece! This is the upload we're testing
				_, _, block, err := peer.ParsePiece(msg.Payload)
				if err == nil {
					gotPieceData <- slices.Clone(block)
				}
				return

			case peer.MsgRequest:
				// They want piece 0 from us
				idx := binary.BigEndian.Uint32(msg.Payload[0:4])
				begin := binary.BigEndian.Uint32(msg.Payload[4:8])
				length := binary.BigEndian.Uint32(msg.Payload[8:12])
				if int(idx) == 0 {
					end := int(begin) + int(length)
					if end > len(piece0Data) {
						end = len(piece0Data)
					}
					pieceMsg := peer.NewPieceMessage(idx, begin, piece0Data[begin:end])
					_ = pieceMsg.Write(conn)
				}

			case peer.MsgInterested:
				_ = (&peer.Message{ID: peer.MsgUnchoke}).Write(conn)
			}
		}
	}()

	// Now test: connect to the fake peer, download piece 0, upload piece 1
	addr := ln.Addr().(*net.TCPAddr)
	p := tracker.Peer{IP: net.IPv4(127, 0, 0, 1), Port: uint16(addr.Port)}

	client, err := NewClient(p, infoHash, ourPeerID)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Send our bitfield: we have piece 1
	ourBF := make(peer.Bitfield, 1)
	ourBF.SetPiece(1)
	if err := client.SendBitfield(ourBF); err != nil {
		t.Fatalf("SendBitfield failed: %v", err)
	}

	// Set up OnRequest to serve piece 1
	client.OnRequest = func(index, begin, length uint32) []byte {
		if int(index) == 1 {
			end := int(begin) + int(length)
			if end > len(piece1Data) {
				end = len(piece1Data)
			}
			return piece1Data[begin:end]
		}
		return nil
	}

	// Download piece 0 from the fake peer
	pw := PieceWork{Index: 0, Hash: piece0Hash, Length: len(piece0Data)}
	data, release, err := client.DownloadPiece(pw)
	if err != nil {
		t.Fatalf("DownloadPiece failed: %v", err)
	}
	if release != nil {
		release()
	}
	if !slices.Equal(data, piece0Data) {
		t.Fatal("downloaded piece 0 data mismatch")
	}

	// Upload happens during or after DownloadPiece. Keep reading messages
	// until the fake peer confirms it received our piece data.
	_ = client.conn.SetDeadline(time.Now().Add(5 * time.Second))
	for i := 0; i < 20; i++ {
		// Check if upload already completed
		select {
		case received := <-gotPieceData:
			if !slices.Equal(received, piece1Data) {
				t.Fatalf("uploaded piece data mismatch: got %d bytes, want %d", len(received), len(piece1Data))
			}
			t.Logf("UPLOAD VERIFIED: peer received %d bytes of piece 1", len(received))
			if client.uploaded.Load() == 0 {
				t.Fatal("client.uploaded is 0 — upload bytes not tracked")
			}
			return
		default:
		}

		msg, err := peer.ReadMessage(client.r)
		if err != nil {
			// Connection may close after upload completes — check one more time
			select {
			case received := <-gotPieceData:
				if !slices.Equal(received, piece1Data) {
					t.Fatalf("uploaded piece data mismatch: got %d bytes, want %d", len(received), len(piece1Data))
				}
				t.Logf("UPLOAD VERIFIED: peer received %d bytes of piece 1", len(received))
				return
			default:
				t.Fatalf("read error and no upload: %v", err)
			}
		}
		if msg == nil {
			continue
		}
		switch msg.ID {
		case peer.MsgInterested:
			_ = client.SendUnchoke()
		case peer.MsgRequest:
			client.handleRequest(msg.Payload)
		}
	}
	t.Fatal("upload did not complete within 20 message reads")
}

// Verify piece hashes array is correct
var _ = [2][20]byte{sha1.Sum([]byte{}), sha1.Sum([]byte{})}

func TestDownloadPieceHashMismatch(t *testing.T) {
	infoHash := [20]byte{0xBB}
	peerID := [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'}

	pieceData := []byte("hello world test data padding!!")
	wrongHash := [20]byte{0xFF} // wrong hash

	pieces := map[int][]byte{0: pieceData}
	ln := fakePeer(t, infoHash, pieces)
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().(*net.TCPAddr)
	p := tracker.Peer{IP: net.IPv4(127, 0, 0, 1), Port: uint16(addr.Port)}

	client, err := NewClient(p, infoHash, peerID)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	pw := PieceWork{Index: 0, Hash: wrongHash, Length: len(pieceData)}
	_, _, err = client.DownloadPiece(pw)
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}
}

func TestDownloadFull(t *testing.T) {
	infoHash := [20]byte{0xCC}
	peerID := [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'}

	// 2 pieces
	piece0 := make([]byte, 256)
	piece1 := make([]byte, 128)
	for i := range piece0 {
		piece0[i] = byte(i)
	}
	for i := range piece1 {
		piece1[i] = byte(i + 100)
	}
	hash0 := sha1.Sum(piece0)
	hash1 := sha1.Sum(piece1)

	pieces := map[int][]byte{0: piece0, 1: piece1}
	ln := fakePeer(t, infoHash, pieces)
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().(*net.TCPAddr)
	peers := []tracker.Peer{{IP: net.IPv4(127, 0, 0, 1), Port: uint16(addr.Port)}}

	tf := &torrent.TorrentFile{
		InfoHash: infoHash,
		Info: torrent.Info{
			Name:        "test",
			PieceLength: 256,
			PieceHashes: [][20]byte{hash0, hash1},
			Length:      int64(len(piece0) + len(piece1)),
		},
	}

	data, err := Download(tf, peerID, peers)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	expected := slices.Concat(piece0, piece1)
	if len(data) != len(expected) {
		t.Fatalf("data length: got %d, want %d", len(data), len(expected))
	}
	for i := range data {
		if data[i] != expected[i] {
			t.Errorf("data mismatch at byte %d", i)
			break
		}
	}
}

func TestDownloadToFileCtx(t *testing.T) {
	infoHash := [20]byte{0xEE}
	peerID := [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'}

	piece0 := make([]byte, 256)
	piece1 := make([]byte, 128)
	for i := range piece0 {
		piece0[i] = byte(i)
	}
	for i := range piece1 {
		piece1[i] = byte(i + 50)
	}
	hash0 := sha1.Sum(piece0)
	hash1 := sha1.Sum(piece1)

	pieces := map[int][]byte{0: piece0, 1: piece1}
	ln := fakePeer(t, infoHash, pieces)
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().(*net.TCPAddr)
	peers := []tracker.Peer{{IP: net.IPv4(127, 0, 0, 1), Port: uint16(addr.Port)}}

	tf := &torrent.TorrentFile{
		InfoHash: infoHash,
		Info: torrent.Info{
			Name:        "ctx-test",
			PieceLength: 256,
			PieceHashes: [][20]byte{hash0, hash1},
			Length:      int64(len(piece0) + len(piece1)),
		},
	}

	outPath := filepath.Join(t.TempDir(), "ctx-test")
	progress := NewProgress(2, int64(len(piece0)+len(piece1)))

	ctx := context.Background()
	err := DownloadToFileCtx(ctx, tf, peerID, peers, outPath, progress)
	if err != nil {
		t.Fatalf("DownloadToFileCtx: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	expected := slices.Concat(piece0, piece1)
	if len(data) != len(expected) {
		t.Fatalf("data length: got %d, want %d", len(data), len(expected))
	}

	snap := progress.Snap()
	if snap.DonePieces != 2 {
		t.Errorf("snap done_pieces: got %d", snap.DonePieces)
	}
}

func TestDownloadToFileCtxCancel(t *testing.T) {
	infoHash := [20]byte{0xFF}
	peerID := [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'}

	hash := sha1.Sum([]byte("x"))
	tf := &torrent.TorrentFile{
		InfoHash: infoHash,
		Info: torrent.Info{
			Name:        "cancel-test",
			PieceLength: 256,
			PieceHashes: [][20]byte{hash},
			Length:      256,
		},
	}

	outPath := filepath.Join(t.TempDir(), "cancel-test")
	progress := NewProgress(1, 256)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := DownloadToFileCtx(ctx, tf, peerID, []tracker.Peer{{IP: net.IPv4(127, 0, 0, 1), Port: 1}}, outPath, progress)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestProgressSnap(t *testing.T) {
	p := NewProgress(10, 1000)
	p.Add(100)
	p.Add(200)
	p.PeerConnect()
	p.PeerConnect()

	snap := p.Snap()
	if snap.DonePieces != 2 {
		t.Errorf("done_pieces: got %d", snap.DonePieces)
	}
	if snap.Downloaded != 300 {
		t.Errorf("downloaded: got %d", snap.Downloaded)
	}
	if snap.ActivePeers != 2 {
		t.Errorf("active_peers: got %d", snap.ActivePeers)
	}
}

func TestDeduplicatePeers(t *testing.T) {
	peers := []tracker.Peer{
		{IP: net.IPv4(1, 2, 3, 4), Port: 6881},
		{IP: net.IPv4(5, 6, 7, 8), Port: 6881},
		{IP: net.IPv4(1, 2, 3, 4), Port: 6881},
	}
	result := deduplicatePeers(peers)
	if len(result) != 2 {
		t.Errorf("dedup: got %d, want 2", len(result))
	}
}

func TestDownloadFullMultiPeer(t *testing.T) {
	infoHash := [20]byte{0xDD}
	peerID := [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'}

	// 4 pieces
	pieceLen := 512
	pieces := make([][]byte, 4)
	hashes := make([][20]byte, 4)
	for i := range pieces {
		pieces[i] = make([]byte, pieceLen)
		for j := range pieces[i] {
			pieces[i][j] = byte(i*100 + j%256)
		}
		hashes[i] = sha1.Sum(pieces[i])
	}

	// Peer 1 has pieces 0, 1
	peer1Pieces := map[int][]byte{0: pieces[0], 1: pieces[1]}
	ln1 := fakePeer(t, infoHash, peer1Pieces)
	defer func() { _ = ln1.Close() }()

	// Peer 2 has pieces 2, 3
	peer2Pieces := map[int][]byte{2: pieces[2], 3: pieces[3]}
	ln2 := fakePeer(t, infoHash, peer2Pieces)
	defer func() { _ = ln2.Close() }()

	addr1 := ln1.Addr().(*net.TCPAddr)
	addr2 := ln2.Addr().(*net.TCPAddr)
	peerList := []tracker.Peer{
		{IP: net.IPv4(127, 0, 0, 1), Port: uint16(addr1.Port)},
		{IP: net.IPv4(127, 0, 0, 1), Port: uint16(addr2.Port)},
	}

	tf := &torrent.TorrentFile{
		InfoHash: infoHash,
		Info: torrent.Info{
			Name:        "test-multi-peer",
			PieceLength: int64(pieceLen),
			PieceHashes: hashes,
			Length:      int64(pieceLen * 4),
		},
	}

	data, err := Download(tf, peerID, peerList)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	expected := slices.Concat(pieces[0], pieces[1], pieces[2], pieces[3])
	if len(data) != len(expected) {
		t.Fatalf("data length: got %d, want %d", len(data), len(expected))
	}
	for i := range data {
		if data[i] != expected[i] {
			t.Errorf("data mismatch at byte %d", i)
			break
		}
	}
}
