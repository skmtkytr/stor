package peer

import (
	"fmt"

	"github.com/skmtkytr/stor/bencode"
)

// MsgExtended is the message ID for BEP 10 extension protocol.
const MsgExtended uint8 = 20

// ExtHandshakeID is the sub-message ID for the extension handshake.
const ExtHandshakeID uint8 = 0

// ExtHandshake represents the BEP 10 extended handshake payload.
type ExtHandshake struct {
	// M maps extension names to local message IDs.
	M map[string]int64
	// MetadataSize is the total size of the torrent metadata (BEP 9).
	MetadataSize int64
	// V is the client version string.
	V string
}

// EncodeExtHandshake encodes an ExtHandshake into a bencoded payload.
func EncodeExtHandshake(h *ExtHandshake) ([]byte, error) {
	d := map[string]any{}

	if len(h.M) > 0 {
		m := make(map[string]any, len(h.M))
		for k, v := range h.M {
			m[k] = v
		}
		d["m"] = m
	}
	if h.MetadataSize > 0 {
		d["metadata_size"] = h.MetadataSize
	}
	if h.V != "" {
		d["v"] = h.V
	}

	return bencode.Encode(d)
}

// DecodeExtHandshake decodes a bencoded payload into an ExtHandshake.
func DecodeExtHandshake(data []byte) (*ExtHandshake, error) {
	decoded, err := bencode.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("peer: ext handshake decode failed: %w", err)
	}

	d, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("peer: ext handshake is not a dict")
	}

	h := &ExtHandshake{}

	if mVal, ok := d["m"]; ok {
		mDict, ok := mVal.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("peer: ext handshake 'm' is not a dict")
		}
		h.M = make(map[string]int64, len(mDict))
		for k, v := range mDict {
			if id, ok := v.(int64); ok {
				h.M[k] = id
			}
		}
	}

	if v, ok := d["metadata_size"].(int64); ok {
		h.MetadataSize = v
	}
	if v, ok := d["v"].(string); ok {
		h.V = v
	}

	return h, nil
}

// NewExtendedMessage creates an extended message (msg ID 20) with a sub-ID and payload.
func NewExtendedMessage(extID uint8, payload []byte) *Message {
	p := make([]byte, 1+len(payload))
	p[0] = extID
	copy(p[1:], payload)
	return &Message{ID: MsgExtended, Payload: p}
}

// ParseExtended extracts the sub-ID and payload from an extended message.
func ParseExtended(payload []byte) (extID uint8, data []byte, err error) {
	if len(payload) < 1 {
		return 0, nil, fmt.Errorf("peer: extended message payload too short")
	}
	return payload[0], payload[1:], nil
}
