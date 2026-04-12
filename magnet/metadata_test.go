package magnet

import (
	"bytes"
	"crypto/sha1"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/skmtkytr/stor/bencode"
	"github.com/skmtkytr/stor/peer"
)

// fakePeerMetadata simulates a peer that serves metadata via BEP 9.
func fakePeerMetadata(t *testing.T, infoHash [20]byte, metadata []byte) net.Listener {
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

		// Handshake
		hs, err := peer.ReadHandshake(conn)
		if err != nil || hs.InfoHash != infoHash {
			return
		}
		_ = peer.WriteHandshake(conn, &peer.Handshake{
			InfoHash:   infoHash,
			PeerID:     [20]byte{'-', 'F', 'K', '0', '0', '0', '1', '-'},
			Extensions: true,
		})

		// Read peer's ext handshake
		for {
			msg, err := peer.ReadMessage(conn)
			if err != nil {
				return
			}
			if msg == nil {
				continue
			}
			if msg.ID != peer.MsgExtended {
				continue
			}
			extID, _, err := peer.ParseExtended(msg.Payload)
			if err != nil {
				return
			}
			if extID == peer.ExtHandshakeID {
				break
			}
		}

		// Send our ext handshake
		ourHS := &peer.ExtHandshake{
			M:            map[string]int64{"ut_metadata": 2},
			MetadataSize: int64(len(metadata)),
			V:            "fake/1.0",
		}
		hsPayload, _ := peer.EncodeExtHandshake(ourHS)
		hsMsg := peer.NewExtendedMessage(peer.ExtHandshakeID, hsPayload)
		_ = hsMsg.Write(conn)

		// Serve metadata requests
		// The peer will send us extended messages with ext ID 2 (our ut_metadata ID)
		for {
			msg, err := peer.ReadMessage(conn)
			if err != nil {
				return
			}
			if msg == nil {
				continue
			}
			if msg.ID != peer.MsgExtended {
				continue
			}
			extID, data, err := peer.ParseExtended(msg.Payload)
			if err != nil {
				return
			}
			if extID != 2 { // our ut_metadata ID
				continue
			}

			metaMsg, err := DecodeMetadataMessage(data)
			if err != nil {
				return
			}
			if metaMsg.MsgType != MetadataRequest {
				continue
			}

			// Send data response
			offset := metaMsg.Piece * MetadataBlockSize
			end := offset + MetadataBlockSize
			if end > len(metadata) {
				end = len(metadata)
			}
			block := metadata[offset:end]

			respPayload, _ := EncodeMetadataData(metaMsg.Piece, len(metadata), block)
			// Send using the peer's ut_metadata ID (1, which the peer told us in their handshake)
			respMsg := peer.NewExtendedMessage(1, respPayload)
			_ = respMsg.Write(conn)
		}
	}()

	return ln
}

func TestMetadataMessageRoundTrip(t *testing.T) {
	// Request
	reqData, err := EncodeMetadataRequest(3)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	reqMsg, err := DecodeMetadataMessage(reqData)
	if err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if reqMsg.MsgType != MetadataRequest || reqMsg.Piece != 3 {
		t.Errorf("request: type=%d piece=%d", reqMsg.MsgType, reqMsg.Piece)
	}

	// Data
	block := bytes.Repeat([]byte("x"), 100)
	dataPayload, err := EncodeMetadataData(2, 31235, block)
	if err != nil {
		t.Fatalf("encode data: %v", err)
	}
	dataMsg, err := DecodeMetadataMessage(dataPayload)
	if err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if dataMsg.MsgType != MetadataData || dataMsg.Piece != 2 || dataMsg.TotalSize != 31235 {
		t.Errorf("data: type=%d piece=%d total=%d", dataMsg.MsgType, dataMsg.Piece, dataMsg.TotalSize)
	}
	if !bytes.Equal(dataMsg.Data, block) {
		t.Errorf("data block mismatch: got %d bytes, want %d", len(dataMsg.Data), len(block))
	}

	// Reject
	rejData, err := EncodeMetadataReject(5)
	if err != nil {
		t.Fatalf("encode reject: %v", err)
	}
	rejMsg, err := DecodeMetadataMessage(rejData)
	if err != nil {
		t.Fatalf("decode reject: %v", err)
	}
	if rejMsg.MsgType != MetadataReject || rejMsg.Piece != 5 {
		t.Errorf("reject: type=%d piece=%d", rejMsg.MsgType, rejMsg.Piece)
	}
}

func TestFetchMetadataOversizedBlock(t *testing.T) {
	// Simulate a malicious peer that sends a metadata block larger than expected.
	// metadataSize=100, but peer sends 16384 bytes — should not overflow the buffer.
	metadata := make([]byte, 100)
	for i := range metadata {
		metadata[i] = byte(i)
	}
	infoHash := sha1.Sum(metadata)

	// Fake peer that sends oversized data block
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

		hs, err := peer.ReadHandshake(conn)
		if err != nil || hs.InfoHash != infoHash {
			return
		}
		_ = peer.WriteHandshake(conn, &peer.Handshake{
			InfoHash: infoHash, PeerID: [20]byte{'-', 'F', 'K'}, Extensions: true,
		})

		// Read ext handshake
		for {
			msg, err := peer.ReadMessage(conn)
			if err != nil {
				return
			}
			if msg != nil && msg.ID == peer.MsgExtended {
				extID, _, _ := peer.ParseExtended(msg.Payload)
				if extID == peer.ExtHandshakeID {
					break
				}
			}
		}

		// Send our ext handshake
		ourHS := &peer.ExtHandshake{
			M: map[string]int64{"ut_metadata": 2}, MetadataSize: 100, V: "evil/1.0",
		}
		hsPayload, _ := peer.EncodeExtHandshake(ourHS)
		_ = peer.NewExtendedMessage(peer.ExtHandshakeID, hsPayload).Write(conn)

		// Wait for metadata request, then send oversized block
		for {
			msg, err := peer.ReadMessage(conn)
			if err != nil {
				return
			}
			if msg == nil || msg.ID != peer.MsgExtended {
				continue
			}
			extID, data, _ := peer.ParseExtended(msg.Payload)
			if extID != 2 {
				continue
			}
			metaMsg, _ := DecodeMetadataMessage(data)
			if metaMsg == nil || metaMsg.MsgType != MetadataRequest {
				continue
			}

			// Send 16KB block even though metadata is only 100 bytes
			bigBlock := make([]byte, MetadataBlockSize)
			respPayload, _ := EncodeMetadataData(metaMsg.Piece, 100, bigBlock)
			_ = peer.NewExtendedMessage(1, respPayload).Write(conn)
			return
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	_ = peer.WriteHandshake(conn, &peer.Handshake{
		InfoHash: infoHash, PeerID: [20]byte{'-', 'S', 'T'}, Extensions: true,
	})
	_, _ = peer.ReadHandshake(conn)

	ourHS := &peer.ExtHandshake{M: map[string]int64{"ut_metadata": 1}, V: "stor/0.1"}
	extConn, peerHS, err := peer.NegotiateExtension(conn.(io.ReadWriter), "ut_metadata", 1, ourHS)
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}

	_, err = FetchMetadata(extConn, int(peerHS.MetadataSize), infoHash)
	if err == nil {
		t.Fatal("expected error for oversized metadata block, got nil")
	}
	// Should be caught by bounds check, not hash mismatch
	if strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("expected block overflow error, got hash mismatch: %v", err)
	}
}

func TestFetchMetadataRejectsHugeSize(t *testing.T) {
	// A malicious peer can claim metadataSize = 2GB to cause OOM.
	// FetchMetadata should reject sizes above MaxMetadataSize.
	_, err := FetchMetadata(nil, 100*1024*1024, [20]byte{}) // 100 MB
	if err == nil {
		t.Fatal("expected error for oversized metadata, got nil")
	}
}

func TestFetchMetadataSmall(t *testing.T) {
	// Create fake metadata (smaller than one block)
	infoDict := map[string]any{
		"name":         "test-file.txt",
		"piece length": int64(262144),
		"length":       int64(1024),
		"pieces":       string(make([]byte, 20)),
	}
	metadata, _ := bencode.Encode(infoDict)
	infoHash := sha1.Sum(metadata)

	ln := fakePeerMetadata(t, infoHash, metadata)
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().(*net.TCPAddr)
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Handshake
	_ = peer.WriteHandshake(conn, &peer.Handshake{
		InfoHash:   infoHash,
		PeerID:     [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Extensions: true,
	})
	_, err = peer.ReadHandshake(conn)
	if err != nil {
		t.Fatalf("handshake failed: %v", err)
	}

	// Negotiate extension
	ourHS := &peer.ExtHandshake{
		M: map[string]int64{"ut_metadata": 1},
		V: "stor/0.1.0",
	}
	extConn, peerHS, err := peer.NegotiateExtension(
		conn.(io.ReadWriter), "ut_metadata", 1, ourHS,
	)
	if err != nil {
		t.Fatalf("negotiate failed: %v", err)
	}

	if peerHS.MetadataSize != int64(len(metadata)) {
		t.Fatalf("metadata size: got %d, want %d", peerHS.MetadataSize, len(metadata))
	}

	// Fetch metadata
	got, err := FetchMetadata(extConn, int(peerHS.MetadataSize), infoHash)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}

	if !bytes.Equal(got, metadata) {
		t.Errorf("metadata mismatch: got %d bytes, want %d", len(got), len(metadata))
	}
}

func TestFetchMetadataMultiBlock(t *testing.T) {
	// Create metadata larger than one block (> 16384 bytes)
	// Use a large name to inflate the bencoded size
	largeName := string(bytes.Repeat([]byte("a"), 20000))
	infoDict := map[string]any{
		"name":         largeName,
		"piece length": int64(262144),
		"length":       int64(1024),
		"pieces":       string(make([]byte, 20)),
	}
	metadata, _ := bencode.Encode(infoDict)
	if len(metadata) <= MetadataBlockSize {
		t.Fatalf("metadata too small for multi-block test: %d bytes", len(metadata))
	}
	infoHash := sha1.Sum(metadata)

	ln := fakePeerMetadata(t, infoHash, metadata)
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().(*net.TCPAddr)
	conn, err := net.Dial("tcp", addr.String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Handshake
	_ = peer.WriteHandshake(conn, &peer.Handshake{
		InfoHash:   infoHash,
		PeerID:     [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-'},
		Extensions: true,
	})
	_, err = peer.ReadHandshake(conn)
	if err != nil {
		t.Fatalf("handshake failed: %v", err)
	}

	ourHS := &peer.ExtHandshake{
		M: map[string]int64{"ut_metadata": 1},
		V: "stor/0.1.0",
	}
	extConn, peerHS, err := peer.NegotiateExtension(
		conn.(io.ReadWriter), "ut_metadata", 1, ourHS,
	)
	if err != nil {
		t.Fatalf("negotiate failed: %v", err)
	}

	got, err := FetchMetadata(extConn, int(peerHS.MetadataSize), infoHash)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}

	if !bytes.Equal(got, metadata) {
		t.Errorf("metadata mismatch: got %d bytes, want %d", len(got), len(metadata))
	}
}
