package dht

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/bits"
	"net"
)

// ID is a 160-bit identifier used for both node IDs and info hashes.
type ID [20]byte

// GenerateID creates a random 160-bit ID.
func GenerateID() ID {
	var id ID
	_, _ = rand.Read(id[:])
	return id
}

// IDFromBytes creates an ID from a byte slice.
func IDFromBytes(b []byte) ID {
	var id ID
	copy(id[:], b)
	return id
}

// IDFromInfoHash creates an ID from a torrent info hash.
func IDFromInfoHash(h [20]byte) ID {
	return ID(h)
}

// String returns the hex-encoded ID.
func (id ID) String() string {
	return hex.EncodeToString(id[:])
}

// Distance returns the XOR distance between two IDs.
func Distance(a, b ID) ID {
	var d ID
	for i := range 20 {
		d[i] = a[i] ^ b[i]
	}
	return d
}

// ZeroPrefixLen returns the number of leading zero bits in the ID.
// This determines which K-bucket a node belongs to.
func (id ID) ZeroPrefixLen() int {
	for i := range 20 {
		if id[i] != 0 {
			return i*8 + bits.LeadingZeros8(id[i])
		}
	}
	return 160
}

// Less returns true if a < b when interpreted as big-endian unsigned integers.
func Less(a, b ID) bool {
	for i := range 20 {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// CompactNodeInfo is 26 bytes: 20-byte node ID + 6-byte compact addr (IPv4:port).
type CompactNodeInfo struct {
	ID   ID
	Addr net.UDPAddr
}

// MarshalCompactNodeInfo encodes a slice of node infos into compact format.
func MarshalCompactNodeInfo(nodes []CompactNodeInfo) []byte {
	buf := make([]byte, 26*len(nodes))
	for i, n := range nodes {
		off := i * 26
		copy(buf[off:off+20], n.ID[:])
		ip := n.Addr.IP.To4()
		if ip == nil {
			continue
		}
		copy(buf[off+20:off+24], ip)
		buf[off+24] = byte(n.Addr.Port >> 8)
		buf[off+25] = byte(n.Addr.Port)
	}
	return buf
}

// UnmarshalCompactNodeInfo decodes compact node info.
func UnmarshalCompactNodeInfo(data []byte) ([]CompactNodeInfo, error) {
	if len(data)%26 != 0 {
		return nil, fmt.Errorf("dht: compact node info length %d is not a multiple of 26", len(data))
	}
	n := len(data) / 26
	nodes := make([]CompactNodeInfo, n)
	for i := range n {
		off := i * 26
		copy(nodes[i].ID[:], data[off:off+20])
		nodes[i].Addr = net.UDPAddr{
			IP:   net.IP(append([]byte{}, data[off+20:off+24]...)),
			Port: int(data[off+24])<<8 | int(data[off+25]),
		}
	}
	return nodes, nil
}
