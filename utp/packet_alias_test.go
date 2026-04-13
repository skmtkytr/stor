package utp

import (
	"bytes"
	"testing"
)

// TestParsePacketPayloadIsolation verifies that modifying the original buffer
// after ParsePacket does NOT corrupt the parsed packet's Payload.
// This catches the buffer-aliasing bug where Payload = data[headerSize:]
// shares the backing array with the caller's buffer.
func TestParsePacketPayloadIsolation(t *testing.T) {
	payload := []byte("hello utp payload")
	pkt := &Packet{
		Header:  Header{Type: StData, Version: 1, ConnID: 1, SeqNr: 1},
		Payload: payload,
	}
	raw := pkt.Marshal()

	parsed, err := ParsePacket(raw)
	if err != nil {
		t.Fatal(err)
	}

	// Save a copy of the parsed payload for comparison
	want := make([]byte, len(parsed.Payload))
	copy(want, parsed.Payload)

	// Simulate buffer reuse (like readLoop does): overwrite the original buffer
	for i := range raw {
		raw[i] = 0xFF
	}

	// The parsed payload must not be affected
	if !bytes.Equal(parsed.Payload, want) {
		t.Errorf("ParsePacket payload corrupted by buffer reuse:\n  got:  %q\n  want: %q", parsed.Payload, want)
	}
}
