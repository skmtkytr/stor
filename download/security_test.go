package download

import (
	"net"
	"testing"

	"github.com/skmtkytr/stor/peer"
)

func TestValidateBitfieldRejectsOversized(t *testing.T) {
	// A malicious peer sends a 1024-byte bitfield for a 10-piece torrent.
	// Expected bitfield size: (10+7)/8 = 2 bytes.
	// ValidateBitfield should reject this.
	c := &Client{
		bitfield: make(peer.Bitfield, 1024),
	}
	if err := c.ValidateBitfield(10); err == nil {
		t.Fatal("expected error for oversized bitfield")
	}
}

func TestValidateBitfieldAcceptsCorrectSize(t *testing.T) {
	// Correct bitfield for 10 pieces: 2 bytes
	c := &Client{
		bitfield: make(peer.Bitfield, 2),
	}
	if err := c.ValidateBitfield(10); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBitfieldAcceptsNilHaveAll(t *testing.T) {
	// nil = have-all (BEP 6), always valid
	c := &Client{
		bitfield: nil,
	}
	if err := c.ValidateBitfield(10); err != nil {
		t.Fatalf("nil bitfield (have-all) should be valid: %v", err)
	}
}

func TestValidateBitfieldAcceptsEmptyHaveNone(t *testing.T) {
	// Empty = have-none (BEP 6), always valid
	c := &Client{
		bitfield: peer.Bitfield{},
	}
	if err := c.ValidateBitfield(10); err != nil {
		t.Fatalf("empty bitfield (have-none) should be valid: %v", err)
	}
}

func TestHandleRequestRejectsInvalidIndex(t *testing.T) {
	// Client with numPieces=2 should reject request for index=999.
	var sentBlock []byte
	c := &Client{
		conn:      &net.TCPConn{},
		numPieces: 2,
		OnRequest: func(index, begin, length uint32) []byte {
			sentBlock = make([]byte, length)
			return sentBlock
		},
	}

	// Request with index=999 (out of range)
	payload := peer.NewRequestMessage(999, 0, 128).Payload
	c.handleRequest(payload)

	// OnRequest should NOT have been called
	if sentBlock != nil {
		t.Fatal("handleRequest should reject index >= numPieces")
	}
}

func TestHandleRequestAllowsValidIndex(t *testing.T) {
	// Client with numPieces=2 should allow request for index=1.
	var called bool
	c := &Client{
		numPieces: 2,
		OnRequest: func(index, begin, length uint32) []byte {
			called = true
			return nil // no data to send
		},
	}

	payload := peer.NewRequestMessage(1, 0, 128).Payload
	c.handleRequest(payload)

	if !called {
		t.Fatal("handleRequest should allow valid index")
	}
}

func TestHandleRequestRejectsZeroNumPieces(t *testing.T) {
	// If numPieces is not set (0), handleRequest should still work
	// by falling through to OnRequest (backward compat).
	var called bool
	c := &Client{
		numPieces: 0,
		OnRequest: func(index, begin, length uint32) []byte {
			called = true
			return nil
		},
	}

	payload := peer.NewRequestMessage(0, 0, 128).Payload
	c.handleRequest(payload)

	if !called {
		t.Fatal("handleRequest with numPieces=0 should still call OnRequest")
	}
}
