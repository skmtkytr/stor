package dht

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"

	"github.com/skmtkytr/stor/bencode"
)

// KRPC message types.
const (
	MsgQuery    = "q"
	MsgResponse = "r"
	MsgError    = "e"
)

// KRPCMessage represents a KRPC protocol message.
type KRPCMessage struct {
	T string         // transaction ID
	Y string         // message type: q, r, e
	Q string         // query method name (for queries)
	A map[string]any // query arguments
	R map[string]any // response values
	E []any          // error [code, description]
}

// EncodeKRPC encodes a KRPC message into bencoded bytes.
func EncodeKRPC(msg *KRPCMessage) ([]byte, error) {
	d := map[string]any{
		"t": msg.T,
		"y": msg.Y,
	}
	switch msg.Y {
	case MsgQuery:
		d["q"] = msg.Q
		d["a"] = msg.A
	case MsgResponse:
		d["r"] = msg.R
	case MsgError:
		d["e"] = msg.E
	}
	return bencode.Encode(d)
}

// DecodeKRPC decodes bencoded bytes into a KRPC message.
func DecodeKRPC(data []byte) (*KRPCMessage, error) {
	decoded, err := bencode.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("dht: krpc decode failed: %w", err)
	}
	d, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("dht: krpc message is not a dict")
	}

	msg := &KRPCMessage{}
	msg.T, _ = d["t"].(string)
	msg.Y, _ = d["y"].(string)
	msg.Q, _ = d["q"].(string)

	if a, ok := d["a"].(map[string]any); ok {
		msg.A = a
	}
	if r, ok := d["r"].(map[string]any); ok {
		msg.R = r
	}
	if e, ok := d["e"].([]any); ok {
		msg.E = e
	}

	return msg, nil
}

// NewTxnID generates a 2-byte random transaction ID.
func NewTxnID() string {
	var buf [2]byte
	_, _ = rand.Read(buf[:])
	return string(buf[:])
}

// NewPingQuery creates a ping query message.
func NewPingQuery(id ID) *KRPCMessage {
	return &KRPCMessage{
		T: NewTxnID(),
		Y: MsgQuery,
		Q: "ping",
		A: map[string]any{"id": string(id[:])},
	}
}

// bep32Want is the `want` parameter requesting both v4 and v6 node replies.
func bep32Want() []any {
	return []any{"n4", "n6"}
}

// NewFindNodeQuery creates a find_node query (BEP 32: requests both v4/v6 nodes).
func NewFindNodeQuery(id, target ID) *KRPCMessage {
	return &KRPCMessage{
		T: NewTxnID(),
		Y: MsgQuery,
		Q: "find_node",
		A: map[string]any{
			"id":     string(id[:]),
			"target": string(target[:]),
			"want":   bep32Want(),
		},
	}
}

// NewGetPeersQuery creates a get_peers query (BEP 32: requests both v4/v6 nodes).
func NewGetPeersQuery(id ID, infoHash ID) *KRPCMessage {
	return &KRPCMessage{
		T: NewTxnID(),
		Y: MsgQuery,
		Q: "get_peers",
		A: map[string]any{
			"id":        string(id[:]),
			"info_hash": string(infoHash[:]),
			"want":      bep32Want(),
		},
	}
}

// NewAnnouncePeerQuery creates an announce_peer query.
func NewAnnouncePeerQuery(id ID, infoHash ID, port int, token string, impliedPort bool) *KRPCMessage {
	var ip int64
	if impliedPort {
		ip = 1
	}
	return &KRPCMessage{
		T: NewTxnID(),
		Y: MsgQuery,
		Q: "announce_peer",
		A: map[string]any{
			"id":           string(id[:]),
			"info_hash":    string(infoHash[:]),
			"port":         int64(port),
			"token":        token,
			"implied_port": ip,
		},
	}
}

// NewResponse creates a response message.
func NewResponse(txnID string, values map[string]any) *KRPCMessage {
	return &KRPCMessage{
		T: txnID,
		Y: MsgResponse,
		R: values,
	}
}

// ParseCompactPeers extracts compact peer addresses from a get_peers values
// list. Each entry is either 6 bytes (IPv4 + port) or 18 bytes (BEP 32: IPv6 +
// port). Other lengths are skipped.
func ParseCompactPeers(values []any) []string {
	var peers []string
	for _, v := range values {
		s, ok := v.(string)
		if !ok {
			continue
		}
		switch len(s) {
		case 6:
			ip := fmt.Sprintf("%d.%d.%d.%d", s[0], s[1], s[2], s[3])
			port := binary.BigEndian.Uint16([]byte(s[4:6]))
			peers = append(peers, fmt.Sprintf("%s:%d", ip, port))
		case 18:
			ip := net.IP(s[:16])
			port := binary.BigEndian.Uint16([]byte(s[16:18]))
			peers = append(peers, fmt.Sprintf("[%s]:%d", ip.String(), port))
		}
	}
	return peers
}
