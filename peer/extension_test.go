package peer

import (
	"bytes"
	"testing"
)

func TestExtHandshakeRoundTrip(t *testing.T) {
	original := &ExtHandshake{
		M: map[string]int64{
			"ut_metadata": 1,
			"ut_pex":      2,
		},
		MetadataSize: 31235,
		V:            "stor/0.1.0",
	}

	data, err := EncodeExtHandshake(original)
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}

	got, err := DecodeExtHandshake(data)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if got.M["ut_metadata"] != 1 {
		t.Errorf("ut_metadata: got %d, want 1", got.M["ut_metadata"])
	}
	if got.M["ut_pex"] != 2 {
		t.Errorf("ut_pex: got %d, want 2", got.M["ut_pex"])
	}
	if got.MetadataSize != 31235 {
		t.Errorf("metadata_size: got %d, want 31235", got.MetadataSize)
	}
	if got.V != "stor/0.1.0" {
		t.Errorf("v: got %q, want %q", got.V, "stor/0.1.0")
	}
}

func TestExtHandshakeMinimal(t *testing.T) {
	h := &ExtHandshake{
		M: map[string]int64{"ut_metadata": 3},
	}

	data, err := EncodeExtHandshake(h)
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}

	got, err := DecodeExtHandshake(data)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if got.M["ut_metadata"] != 3 {
		t.Errorf("ut_metadata: got %d", got.M["ut_metadata"])
	}
	if got.MetadataSize != 0 {
		t.Errorf("metadata_size should be 0, got %d", got.MetadataSize)
	}
	if got.V != "" {
		t.Errorf("v should be empty, got %q", got.V)
	}
}

func TestNewExtendedMessage(t *testing.T) {
	payload := []byte("test payload")
	msg := NewExtendedMessage(5, payload)

	if msg.ID != MsgExtended {
		t.Errorf("msg ID: got %d, want %d", msg.ID, MsgExtended)
	}
	if msg.Payload[0] != 5 {
		t.Errorf("ext ID: got %d, want 5", msg.Payload[0])
	}
	if !bytes.Equal(msg.Payload[1:], payload) {
		t.Errorf("payload mismatch")
	}
}

func TestParseExtended(t *testing.T) {
	payload := []byte{3, 'h', 'e', 'l', 'l', 'o'}
	extID, data, err := ParseExtended(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extID != 3 {
		t.Errorf("ext ID: got %d, want 3", extID)
	}
	if string(data) != "hello" {
		t.Errorf("data: got %q, want %q", data, "hello")
	}
}

func TestParseExtendedEmpty(t *testing.T) {
	_, _, err := ParseExtended([]byte{})
	if err == nil {
		t.Fatal("expected error for empty payload")
	}
}

func TestHandshakeExtensionBit(t *testing.T) {
	h := &Handshake{
		InfoHash:   [20]byte{1},
		PeerID:     [20]byte{2},
		Extensions: true,
	}

	var buf bytes.Buffer
	if err := WriteHandshake(&buf, h); err != nil {
		t.Fatalf("write error: %v", err)
	}

	// Check reserved byte 5 has bit 4 set
	data := buf.Bytes()
	if data[25]&0x10 == 0 {
		t.Error("extension bit should be set in reserved[5]")
	}

	got, err := ReadHandshake(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if !got.Extensions {
		t.Error("Extensions should be true after round-trip")
	}
}

func TestHandshakeNoExtensionBit(t *testing.T) {
	h := &Handshake{
		InfoHash:   [20]byte{1},
		PeerID:     [20]byte{2},
		Extensions: false,
	}

	var buf bytes.Buffer
	if err := WriteHandshake(&buf, h); err != nil {
		t.Fatalf("write error: %v", err)
	}

	got, err := ReadHandshake(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if got.Extensions {
		t.Error("Extensions should be false")
	}
}

func TestExtendedMessageRoundTrip(t *testing.T) {
	h := &ExtHandshake{
		M: map[string]int64{"ut_metadata": 1},
		V: "stor/0.1.0",
	}
	payload, err := EncodeExtHandshake(h)
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}

	msg := NewExtendedMessage(ExtHandshakeID, payload)

	var buf bytes.Buffer
	if err := msg.Write(&buf); err != nil {
		t.Fatalf("write error: %v", err)
	}

	got, err := ReadMessage(&buf)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	if got.ID != MsgExtended {
		t.Fatalf("msg ID: got %d, want %d", got.ID, MsgExtended)
	}

	extID, data, err := ParseExtended(got.Payload)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if extID != ExtHandshakeID {
		t.Errorf("ext ID: got %d, want %d", extID, ExtHandshakeID)
	}

	gotH, err := DecodeExtHandshake(data)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if gotH.M["ut_metadata"] != 1 {
		t.Errorf("ut_metadata: got %d", gotH.M["ut_metadata"])
	}
}
