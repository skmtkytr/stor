// Package utp implements the Micro Transport Protocol (uTP, BEP 29)
// over UDP with LEDBAT congestion control.
package utp

import (
	"encoding/binary"
	"fmt"
)

// Packet types (first nibble of type_ver byte).
const (
	StData  uint8 = 0
	StFin   uint8 = 1
	StState uint8 = 2
	StReset uint8 = 3
	StSyn   uint8 = 4
)

// Header is a 20-byte uTP packet header.
type Header struct {
	Type      uint8
	Version   uint8
	Extension uint8
	ConnID    uint16
	Timestamp uint32 // microseconds
	TSDiff    uint32 // microsecond difference
	WndSize   uint32 // advertised window in bytes
	SeqNr     uint16
	AckNr     uint16
}

const headerSize = 20

// Packet is a uTP packet with header and payload.
type Packet struct {
	Header  Header
	Payload []byte
}

// MarshalHeader serializes the header into 20 bytes.
func (h *Header) Marshal() []byte {
	buf := make([]byte, headerSize)
	buf[0] = (h.Type << 4) | (h.Version & 0x0f)
	buf[1] = h.Extension
	binary.BigEndian.PutUint16(buf[2:4], h.ConnID)
	binary.BigEndian.PutUint32(buf[4:8], h.Timestamp)
	binary.BigEndian.PutUint32(buf[8:12], h.TSDiff)
	binary.BigEndian.PutUint32(buf[12:16], h.WndSize)
	binary.BigEndian.PutUint16(buf[16:18], h.SeqNr)
	binary.BigEndian.PutUint16(buf[18:20], h.AckNr)
	return buf
}

// ParseHeader parses a 20-byte header from raw data.
func ParseHeader(data []byte) (*Header, error) {
	if len(data) < headerSize {
		return nil, fmt.Errorf("utp: packet too short: %d bytes", len(data))
	}
	h := &Header{
		Type:      data[0] >> 4,
		Version:   data[0] & 0x0f,
		Extension: data[1],
		ConnID:    binary.BigEndian.Uint16(data[2:4]),
		Timestamp: binary.BigEndian.Uint32(data[4:8]),
		TSDiff:    binary.BigEndian.Uint32(data[8:12]),
		WndSize:   binary.BigEndian.Uint32(data[12:16]),
		SeqNr:     binary.BigEndian.Uint16(data[16:18]),
		AckNr:     binary.BigEndian.Uint16(data[18:20]),
	}
	return h, nil
}

// Marshal serializes the packet.
func (p *Packet) Marshal() []byte {
	hdr := p.Header.Marshal()
	if len(p.Payload) == 0 {
		return hdr
	}
	buf := make([]byte, headerSize+len(p.Payload))
	copy(buf, hdr)
	copy(buf[headerSize:], p.Payload)
	return buf
}

// ParsePacket parses a raw UDP payload into a uTP packet.
func ParsePacket(data []byte) (*Packet, error) {
	h, err := ParseHeader(data)
	if err != nil {
		return nil, err
	}
	p := &Packet{Header: *h}
	if len(data) > headerSize {
		p.Payload = data[headerSize:]
	}
	return p, nil
}

// IsUTP checks if a UDP packet looks like a uTP packet (version == 1).
func IsUTP(data []byte) bool {
	if len(data) < headerSize {
		return false
	}
	version := data[0] & 0x0f
	return version == 1
}
