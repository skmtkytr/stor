package magnet

import (
	"crypto/sha1"
	"fmt"

	"github.com/skmtkytr/stor/bencode"
	"github.com/skmtkytr/stor/peer"
)

const (
	// MetadataBlockSize is the maximum metadata block size (16 KiB) per BEP 9.
	MetadataBlockSize = 16384

	// Metadata message types.
	MetadataRequest uint8 = 0
	MetadataData    uint8 = 1
	MetadataReject  uint8 = 2
)

// EncodeMetadataRequest creates a ut_metadata request message for the given piece.
func EncodeMetadataRequest(piece int) ([]byte, error) {
	d := map[string]any{
		"msg_type": int64(MetadataRequest),
		"piece":    int64(piece),
	}
	return bencode.Encode(d)
}

// EncodeMetadataData creates a ut_metadata data message.
// The returned bytes are: bencoded dict + raw metadata block.
func EncodeMetadataData(piece int, totalSize int, block []byte) ([]byte, error) {
	d := map[string]any{
		"msg_type":   int64(MetadataData),
		"piece":      int64(piece),
		"total_size": int64(totalSize),
	}
	encoded, err := bencode.Encode(d)
	if err != nil {
		return nil, err
	}
	return append(encoded, block...), nil
}

// EncodeMetadataReject creates a ut_metadata reject message.
func EncodeMetadataReject(piece int) ([]byte, error) {
	d := map[string]any{
		"msg_type": int64(MetadataReject),
		"piece":    int64(piece),
	}
	return bencode.Encode(d)
}

// MetadataMessage represents a parsed ut_metadata message.
type MetadataMessage struct {
	MsgType   uint8
	Piece     int
	TotalSize int
	Data      []byte // raw metadata block (only for MsgType == MetadataData)
}

// DecodeMetadataMessage parses a ut_metadata message payload.
// For data messages, the payload is: bencoded dict + raw metadata bytes.
func DecodeMetadataMessage(payload []byte) (*MetadataMessage, error) {
	// The dict may be followed by raw data, so we use DecodeBytes
	decoded, consumed, err := bencode.DecodeBytes(payload)
	if err != nil {
		return nil, fmt.Errorf("magnet: metadata message decode failed: %w", err)
	}

	d, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("magnet: metadata message is not a dict")
	}

	msg := &MetadataMessage{}

	if v, ok := d["msg_type"].(int64); ok {
		msg.MsgType = uint8(v)
	}
	if v, ok := d["piece"].(int64); ok {
		msg.Piece = int(v)
	}
	if v, ok := d["total_size"].(int64); ok {
		msg.TotalSize = int(v)
	}

	// For data messages, the remaining bytes after the dict are the metadata block
	if msg.MsgType == MetadataData && consumed < len(payload) {
		msg.Data = payload[consumed:]
	}

	return msg, nil
}

// FetchMetadata fetches the full info dict metadata from a peer that supports
// BEP 9 ut_metadata extension. Returns the raw bencoded info dict.
func FetchMetadata(conn *peer.ExtConn, metadataSize int, infoHash [20]byte) ([]byte, error) {
	numPieces := (metadataSize + MetadataBlockSize - 1) / MetadataBlockSize
	metadata := make([]byte, metadataSize)

	for i := range numPieces {
		// Send request
		reqPayload, err := EncodeMetadataRequest(i)
		if err != nil {
			return nil, fmt.Errorf("magnet: encode request failed: %w", err)
		}

		if err := conn.SendExtMessage(reqPayload); err != nil {
			return nil, fmt.Errorf("magnet: send request failed: %w", err)
		}

		// Read response
		respPayload, err := conn.ReadExtMessage()
		if err != nil {
			return nil, fmt.Errorf("magnet: read response failed: %w", err)
		}

		msg, err := DecodeMetadataMessage(respPayload)
		if err != nil {
			return nil, err
		}

		if msg.MsgType == MetadataReject {
			return nil, fmt.Errorf("magnet: peer rejected metadata request for piece %d", i)
		}
		if msg.MsgType != MetadataData {
			return nil, fmt.Errorf("magnet: unexpected message type %d for piece %d", msg.MsgType, i)
		}

		offset := i * MetadataBlockSize
		copy(metadata[offset:], msg.Data)
	}

	// Verify info hash
	hash := sha1.Sum(metadata)
	if hash != infoHash {
		return nil, fmt.Errorf("magnet: metadata info hash mismatch")
	}

	return metadata, nil
}
