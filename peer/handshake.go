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
}

// WriteHandshake writes the 68-byte handshake to the writer.
func WriteHandshake(w io.Writer, h *Handshake) error {
	buf := make([]byte, 68)
	buf[0] = 19 // pstrlen
	copy(buf[1:20], protocolID)
	// buf[20:28] reserved (zeros)
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

	h := &Handshake{}
	copy(h.InfoHash[:], buf[28:48])
	copy(h.PeerID[:], buf[48:68])
	return h, nil
}
