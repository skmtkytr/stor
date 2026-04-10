package peer

import (
	"fmt"
	"io"
)

const (
	protocolID = "BitTorrent protocol"
)

// Handshake represents a BitTorrent peer handshake (68 bytes).
type Handshake struct {
	InfoHash [20]byte
	PeerID   [20]byte
	// Extensions indicates BEP 10 extension protocol support.
	Extensions bool
}

// WriteHandshake writes the 68-byte handshake to the writer.
func WriteHandshake(w io.Writer, h *Handshake) error {
	buf := make([]byte, 68)
	buf[0] = 19 // pstrlen
	copy(buf[1:20], protocolID)
	// reserved bytes [20:28]
	if h.Extensions {
		buf[25] |= 0x10 // bit 20 from the right = byte 5, bit 4
	}
	copy(buf[28:48], h.InfoHash[:])
	copy(buf[48:68], h.PeerID[:])
	_, err := w.Write(buf)
	return err
}

// ReadHandshake reads a 68-byte handshake from the reader.
func ReadHandshake(r io.Reader) (*Handshake, error) {
	buf := make([]byte, 68)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("peer: handshake read failed: %w", err)
	}

	if buf[0] != 19 {
		return nil, fmt.Errorf("peer: unexpected pstrlen: %d", buf[0])
	}
	if string(buf[1:20]) != protocolID {
		return nil, fmt.Errorf("peer: unexpected protocol: %q", buf[1:20])
	}

	h := &Handshake{
		Extensions: buf[25]&0x10 != 0,
	}
	copy(h.InfoHash[:], buf[28:48])
	copy(h.PeerID[:], buf[48:68])
	return h, nil
}
