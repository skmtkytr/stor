package peer

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestHandshakeRoundTrip(t *testing.T) {
	original := &Handshake{
		InfoHash: [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		PeerID:   [20]byte{'-', 'S', 'T', '0', '0', '0', '1', '-', 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l'},
	}

	var buf bytes.Buffer
	if err := WriteHandshake(&buf, original); err != nil {
		t.Fatalf("write error: %v", err)
	}

	if buf.Len() != 68 {
		t.Fatalf("handshake size: got %d, want 68", buf.Len())
	}

	got, err := ReadHandshake(&buf)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	if got.InfoHash != original.InfoHash {
		t.Errorf("info hash mismatch")
	}
	if got.PeerID != original.PeerID {
		t.Errorf("peer id mismatch")
	}
}

func TestHandshakeProtocolString(t *testing.T) {
	h := &Handshake{}
	var buf bytes.Buffer
	WriteHandshake(&buf, h)

	data := buf.Bytes()
	if data[0] != 19 {
		t.Errorf("pstrlen: got %d, want 19", data[0])
	}
	if string(data[1:20]) != "BitTorrent protocol" {
		t.Errorf("pstr: got %q", data[1:20])
	}
}

func TestHandshakeInvalidPstrlen(t *testing.T) {
	buf := make([]byte, 68)
	buf[0] = 18 // wrong
	copy(buf[1:20], "BitTorrent protoco")

	_, err := ReadHandshake(bytes.NewReader(buf))
	if err == nil {
		t.Fatal("expected error for invalid pstrlen")
	}
}

func TestMessageRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
	}{
		{"choke", Message{ID: MsgChoke}},
		{"unchoke", Message{ID: MsgUnchoke}},
		{"interested", Message{ID: MsgInterested}},
		{"have", *NewHaveMessage(42)},
		{"request", *NewRequestMessage(1, 0, 16384)},
		{"piece", Message{ID: MsgPiece, Payload: append(make([]byte, 8), []byte("block data")...)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tt.msg.Write(&buf); err != nil {
				t.Fatalf("write error: %v", err)
			}

			got, err := ReadMessage(&buf)
			if err != nil {
				t.Fatalf("read error: %v", err)
			}
			if got.ID != tt.msg.ID {
				t.Errorf("ID: got %d, want %d", got.ID, tt.msg.ID)
			}
			if !bytes.Equal(got.Payload, tt.msg.Payload) {
				t.Errorf("payload mismatch")
			}
		})
	}
}

func TestKeepAlive(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteKeepAlive(&buf); err != nil {
		t.Fatalf("write error: %v", err)
	}
	if buf.Len() != 4 {
		t.Fatalf("keep-alive size: got %d, want 4", buf.Len())
	}

	msg, err := ReadMessage(&buf)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if msg != nil {
		t.Errorf("keep-alive should return nil message, got %+v", msg)
	}
}

func TestParsePiece(t *testing.T) {
	payload := make([]byte, 8+5)
	binary.BigEndian.PutUint32(payload[0:4], 3)     // index
	binary.BigEndian.PutUint32(payload[4:8], 16384) // begin
	copy(payload[8:], "hello")

	index, begin, block, err := ParsePiece(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != 3 {
		t.Errorf("index: got %d", index)
	}
	if begin != 16384 {
		t.Errorf("begin: got %d", begin)
	}
	if string(block) != "hello" {
		t.Errorf("block: got %q", block)
	}
}

func TestParsePieceTooShort(t *testing.T) {
	_, _, _, err := ParsePiece([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for short payload")
	}
}

func TestBitfield(t *testing.T) {
	bf := Bitfield(make([]byte, 2)) // 16 bits

	if bf.HasPiece(0) {
		t.Error("piece 0 should not be set")
	}

	bf.SetPiece(0)
	if !bf.HasPiece(0) {
		t.Error("piece 0 should be set")
	}

	bf.SetPiece(7)
	if !bf.HasPiece(7) {
		t.Error("piece 7 should be set")
	}

	bf.SetPiece(15)
	if !bf.HasPiece(15) {
		t.Error("piece 15 should be set")
	}

	if bf.HasPiece(1) {
		t.Error("piece 1 should not be set")
	}

	// Out of range
	if bf.HasPiece(16) {
		t.Error("piece 16 should not be set (out of range)")
	}
}

func TestBitfieldSetPiece(t *testing.T) {
	bf := Bitfield([]byte{0b10000000, 0b00000000})
	if !bf.HasPiece(0) {
		t.Error("piece 0 should be set")
	}
	if bf.HasPiece(1) {
		t.Error("piece 1 should not be set")
	}
	bf.SetPiece(1)
	if !bf.HasPiece(1) {
		t.Error("piece 1 should be set after SetPiece")
	}
	// Original bit should still be set
	if !bf.HasPiece(0) {
		t.Error("piece 0 should still be set")
	}
}

func TestParseHave(t *testing.T) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, 42)

	index, err := ParseHave(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != 42 {
		t.Errorf("got %d, want 42", index)
	}
}

func TestParseHaveInvalid(t *testing.T) {
	_, err := ParseHave([]byte{1, 2})
	if err == nil {
		t.Fatal("expected error for invalid payload")
	}
}
