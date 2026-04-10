package peer

import (
	"fmt"
	"io"
)

// ExtConn wraps a peer connection for BEP 10 extended message exchange.
// It knows the local and remote extension message IDs for a specific extension.
type ExtConn struct {
	rw          io.ReadWriter
	localExtID  uint8 // our ext msg ID that remote should use
	remoteExtID uint8 // remote's ext msg ID that we should use
}

// NewExtConn creates a new ExtConn.
// localExtID is the ID we told the remote peer to use when sending to us.
// remoteExtID is the ID the remote peer told us to use when sending to them.
func NewExtConn(rw io.ReadWriter, localExtID, remoteExtID uint8) *ExtConn {
	return &ExtConn{
		rw:          rw,
		localExtID:  localExtID,
		remoteExtID: remoteExtID,
	}
}

// SendExtMessage sends an extended message using the remote peer's extension ID.
func (c *ExtConn) SendExtMessage(payload []byte) error {
	msg := NewExtendedMessage(c.remoteExtID, payload)
	return msg.Write(c.rw)
}

// ReadExtMessage reads messages until it receives an extended message with our local ext ID.
// It returns the extension payload (without the ext ID byte).
// Non-matching messages are silently discarded.
func (c *ExtConn) ReadExtMessage() ([]byte, error) {
	for {
		msg, err := ReadMessage(c.rw)
		if err != nil {
			return nil, err
		}
		if msg == nil {
			continue // keep-alive
		}
		if msg.ID != MsgExtended {
			continue // not an extended message
		}

		extID, data, err := ParseExtended(msg.Payload)
		if err != nil {
			return nil, err
		}
		if extID != c.localExtID {
			continue // not for our extension
		}

		return data, nil
	}
}

// ReadWriter returns the underlying ReadWriter for sending non-extension messages.
func (c *ExtConn) ReadWriter() io.ReadWriter {
	return c.rw
}

// NegotiateExtension performs BEP 10 extended handshake and returns an ExtConn
// for the specified extension name (e.g., "ut_metadata").
// ourID is the local message ID we want to assign to the extension.
func NegotiateExtension(rw io.ReadWriter, extName string, ourID uint8, ourHandshake *ExtHandshake) (*ExtConn, *ExtHandshake, error) {
	// Send our extended handshake
	hsPayload, err := EncodeExtHandshake(ourHandshake)
	if err != nil {
		return nil, nil, fmt.Errorf("peer: encode ext handshake failed: %w", err)
	}
	hsMsg := NewExtendedMessage(ExtHandshakeID, hsPayload)
	if err := hsMsg.Write(rw); err != nil {
		return nil, nil, fmt.Errorf("peer: send ext handshake failed: %w", err)
	}

	// Read peer's extended handshake
	for {
		msg, err := ReadMessage(rw)
		if err != nil {
			return nil, nil, fmt.Errorf("peer: read ext handshake failed: %w", err)
		}
		if msg == nil {
			continue // keep-alive
		}
		if msg.ID != MsgExtended {
			continue // skip non-extended messages (bitfield, have, etc.)
		}

		extID, data, err := ParseExtended(msg.Payload)
		if err != nil {
			return nil, nil, err
		}
		if extID != ExtHandshakeID {
			continue // not a handshake
		}

		peerHS, err := DecodeExtHandshake(data)
		if err != nil {
			return nil, nil, err
		}

		remoteID, ok := peerHS.M[extName]
		if !ok || remoteID == 0 {
			return nil, nil, fmt.Errorf("peer: remote does not support %q extension", extName)
		}

		conn := NewExtConn(rw, ourID, uint8(remoteID))
		return conn, peerHS, nil
	}
}
