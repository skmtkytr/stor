package peer

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
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

// BEP 6: Fast Extension message IDs.
const (
	MsgSuggest     uint8 = 13
	MsgHaveAll     uint8 = 14
	MsgHaveNone    uint8 = 15
	MsgReject      uint8 = 16
	MsgAllowedFast uint8 = 17
)

// Message represents a peer wire protocol message.
type Message struct {
	ID      uint8
	Payload []byte
}

// msgBufPool reuses 4-byte buffers for length prefix reads.
var msgBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 4)
		return &b
	},
}

// ReadMessage reads a single message from the reader.
// A keep-alive (length=0) returns nil, nil.
func ReadMessage(r io.Reader) (*Message, error) {
	lenBufPtr := msgBufPool.Get().(*[]byte)
	lenBuf := *lenBufPtr
	defer msgBufPool.Put(lenBufPtr)

	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(lenBuf)

	// Keep-alive
	if length == 0 {
		return nil, nil
	}

	// Sanity check: messages shouldn't exceed ~16KiB block + 13 byte header,
	// but metadata messages can be larger. Cap at 4MB.
	if length > 4*1024*1024 {
		return nil, fmt.Errorf("peer: message too large: %d bytes", length)
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

// Write serializes the message into a single write.
func (m *Message) Write(w io.Writer) error {
	length := 1 + len(m.Payload)
	buf := make([]byte, 4+length)
	binary.BigEndian.PutUint32(buf[0:4], uint32(length))
	buf[4] = m.ID
	copy(buf[5:], m.Payload)
	_, err := w.Write(buf)
	return err
}

// WriteKeepAlive writes a keep-alive message (4 zero bytes).
func WriteKeepAlive(w io.Writer) error {
	_, err := w.Write([]byte{0, 0, 0, 0})
	return err
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

// NewPieceMessage creates a piece message (index, begin, block data).
func NewPieceMessage(index, begin uint32, block []byte) *Message {
	payload := make([]byte, 8+len(block))
	binary.BigEndian.PutUint32(payload[0:4], index)
	binary.BigEndian.PutUint32(payload[4:8], begin)
	copy(payload[8:], block)
	return &Message{ID: MsgPiece, Payload: payload}
}

// NewCancelMessage creates a cancel message (index, begin, length).
func NewCancelMessage(index, begin, length uint32) *Message {
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload[0:4], index)
	binary.BigEndian.PutUint32(payload[4:8], begin)
	binary.BigEndian.PutUint32(payload[8:12], length)
	return &Message{ID: MsgCancel, Payload: payload}
}

// ParseRequest extracts index, begin, and length from a request message payload.
func ParseRequest(payload []byte) (index, begin, length uint32, err error) {
	if len(payload) < 12 {
		return 0, 0, 0, fmt.Errorf("peer: request payload too short: %d bytes", len(payload))
	}
	index = binary.BigEndian.Uint32(payload[0:4])
	begin = binary.BigEndian.Uint32(payload[4:8])
	length = binary.BigEndian.Uint32(payload[8:12])
	return
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

// BEP 6: Fast Extension message constructors and parsers.

// NewRejectMessage creates a reject message (same format as request/cancel).
func NewRejectMessage(index, begin, length uint32) *Message {
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload[0:4], index)
	binary.BigEndian.PutUint32(payload[4:8], begin)
	binary.BigEndian.PutUint32(payload[8:12], length)
	return &Message{ID: MsgReject, Payload: payload}
}

// ParseReject extracts index, begin, length from a reject payload.
func ParseReject(payload []byte) (index, begin, length uint32, err error) {
	return ParseRequest(payload) // same format
}

// NewAllowedFastMessage creates an allowed-fast message.
func NewAllowedFastMessage(index uint32) *Message {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, index)
	return &Message{ID: MsgAllowedFast, Payload: payload}
}

// ParseAllowedFast extracts the piece index from an allowed-fast payload.
func ParseAllowedFast(payload []byte) (uint32, error) {
	return ParseHave(payload) // same format
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
