package download

import (
	"crypto/sha1"
	"encoding/binary"
	"net"
	"testing"

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
		defer conn.Close()

		// Read handshake
		hs, err := peer.ReadHandshake(conn)
		if err != nil {
			return
		}
		if hs.InfoHash != infoHash {
			return
		}

		// Write handshake
		peer.WriteHandshake(conn, &peer.Handshake{
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
		bfMsg.Write(conn)

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
				unchoke.Write(conn)
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

				payload := make([]byte, 8+len(block))
				binary.BigEndian.PutUint32(payload[0:4], idx)
				binary.BigEndian.PutUint32(payload[4:8], begin)
				copy(payload[8:], block)

				pieceMsg := &peer.Message{ID: peer.MsgPiece, Payload: payload}
				pieceMsg.Write(conn)
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
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	p := tracker.Peer{IP: net.IPv4(127, 0, 0, 1), Port: uint16(addr.Port)}

	client, err := NewClient(p, infoHash, peerID)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	if !client.HasPiece(0) {
		t.Fatal("client should report having piece 0")
	}

	pw := PieceWork{Index: 0, Hash: pieceHash, Length: len(pieceData)}
	data, err := client.DownloadPiece(pw)
	if err != nil {
		t.Fatalf("DownloadPiece failed: %v", err)
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

func TestDownloadPieceHashMismatch(t *testing.T) {
	infoHash := [20]byte{0xBB}
	peerID := [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'}

	pieceData := []byte("hello world test data padding!!")
	wrongHash := [20]byte{0xFF} // wrong hash

	pieces := map[int][]byte{0: pieceData}
	ln := fakePeer(t, infoHash, pieces)
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	p := tracker.Peer{IP: net.IPv4(127, 0, 0, 1), Port: uint16(addr.Port)}

	client, err := NewClient(p, infoHash, peerID)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	pw := PieceWork{Index: 0, Hash: wrongHash, Length: len(pieceData)}
	_, err = client.DownloadPiece(pw)
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
	defer ln.Close()

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

	expected := append(piece0, piece1...)
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
