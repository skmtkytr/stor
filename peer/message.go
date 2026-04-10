package peer

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Message IDs as defined in BEP 3.
const (
	MsgChoke         uint8 = 0
	MsgUnchoke       uint8 = 1
	MsgInterested    uint8 = 2
	MsgNotInterested uint8 = 3
	MsgHave          uint8 = 4
	MsgBitfield      uint8 = 5
	MsgRequest       uint8 = 6
	MsgPiece         uint8 = 7
	MsgCancel        uint8 = 8
)

// Message represents a peer wire protocol message.
type Message struct {
	ID      uint8
	Payload []byte
}

// ReadMessage reads a single message from the reader.
// A keep-alive (length=0) returns nil, nil.
func ReadMessage(r io.Reader) (*Message, error) {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, err
	}

	// Keep-alive
	if length == 0 {
		return nil, nil
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}

	return &Message{
		ID:      buf[0],
		Payload: buf[1:],
	}, nil
}

// Write serializes the message and writes it to the writer.
func (m *Message) Write(w io.Writer) error {
	length := uint32(1 + len(m.Payload))
	if err := binary.Write(w, binary.BigEndian, length); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, m.ID); err != nil {
		return err
	}
	if len(m.Payload) > 0 {
		if _, err := w.Write(m.Payload); err != nil {
			return err
		}
	}
	return nil
}

// WriteKeepAlive writes a keep-alive message (4 zero bytes).
func WriteKeepAlive(w io.Writer) error {
	return binary.Write(w, binary.BigEndian, uint32(0))
}

// NewRequestMessage creates a request message (index, begin, length).
func NewRequestMessage(index, begin, length uint32) *Message {
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload[0:4], index)
	binary.BigEndian.PutUint32(payload[4:8], begin)
	binary.BigEndian.PutUint32(payload[8:12], length)
	return &Message{ID: MsgRequest, Payload: payload}
}

// NewHaveMessage creates a have message.
func NewHaveMessage(index uint32) *Message {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, index)
	return &Message{ID: MsgHave, Payload: payload}
}

// ParsePiece extracts index, begin, and block from a piece message payload.
func ParsePiece(payload []byte) (index, begin uint32, block []byte, err error) {
	if len(payload) < 8 {
		return 0, 0, nil, fmt.Errorf("peer: piece payload too short: %d bytes", len(payload))
	}
	index = binary.BigEndian.Uint32(payload[0:4])
	begin = binary.BigEndian.Uint32(payload[4:8])
	block = payload[8:]
	return
}

// ParseHave extracts the piece index from a have message payload.
func ParseHave(payload []byte) (uint32, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("peer: have payload must be 4 bytes, got %d", len(payload))
	}
	return binary.BigEndian.Uint32(payload), nil
}

// Bitfield represents which pieces a peer has.
type Bitfield []byte

// HasPiece returns true if the bitfield indicates the peer has the given piece.
func (bf Bitfield) HasPiece(index int) bool {
	byteIndex := index / 8
	offset := index % 8
	if byteIndex >= len(bf) {
		return false
	}
	return bf[byteIndex]>>(7-offset)&1 != 0
}

// SetPiece sets the bit for the given piece index.
func (bf Bitfield) SetPiece(index int) {
	byteIndex := index / 8
	offset := index % 8
	if byteIndex >= len(bf) {
		return
	}
	bf[byteIndex] |= 1 << (7 - offset)
}
