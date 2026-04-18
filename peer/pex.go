package peer

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/skmtkytr/stor/bencode"
)

// maxPEXPeers is the maximum number of peers in a single PEX message.
const maxPEXPeers = 10000

// PEXMessage represents a ut_pex message (BEP 11).
type PEXMessage struct {
	Added   []PEXPeer
	Dropped []PEXPeer
}

// PEXPeer is a peer discovered via PEX.
type PEXPeer struct {
	IP   net.IP
	Port uint16
	Seed bool
}

// EncodePEX encodes a PEX message into bencoded payload.
// IPv4 peers go into "added"/"dropped" (6 bytes each, BEP 11).
// IPv6 peers go into "added6"/"dropped6" (18 bytes each, BEP 11 extension).
func EncodePEX(msg *PEXMessage) ([]byte, error) {
	d := map[string]any{}

	if len(msg.Added) > 0 {
		addedV4, flagsV4 := encodeAddedV4(msg.Added)
		addedV6, flagsV6 := encodeAddedV6(msg.Added)
		if len(addedV4) > 0 {
			d["added"] = string(addedV4)
			d["added.f"] = string(flagsV4)
		}
		if len(addedV6) > 0 {
			d["added6"] = string(addedV6)
			d["added6.f"] = string(flagsV6)
		}
	}

	if len(msg.Dropped) > 0 {
		droppedV4 := encodeDroppedV4(msg.Dropped)
		droppedV6 := encodeDroppedV6(msg.Dropped)
		if len(droppedV4) > 0 {
			d["dropped"] = string(droppedV4)
		}
		if len(droppedV6) > 0 {
			d["dropped6"] = string(droppedV6)
		}
	}

	return bencode.Encode(d)
}

func encodeAddedV4(peers []PEXPeer) (compact, flags []byte) {
	compact = make([]byte, 0, len(peers)*6)
	flags = make([]byte, 0, len(peers))
	for _, p := range peers {
		ip4 := p.IP.To4()
		if ip4 == nil {
			continue
		}
		var buf [6]byte
		copy(buf[:4], ip4)
		binary.BigEndian.PutUint16(buf[4:], p.Port)
		compact = append(compact, buf[:]...)
		var f byte
		if p.Seed {
			f |= 0x02
		}
		flags = append(flags, f)
	}
	return compact, flags
}

func encodeAddedV6(peers []PEXPeer) (compact, flags []byte) {
	compact = make([]byte, 0, len(peers)*18)
	flags = make([]byte, 0, len(peers))
	for _, p := range peers {
		if p.IP.To4() != nil {
			continue // IPv4 — encoded separately
		}
		ip16 := p.IP.To16()
		if ip16 == nil {
			continue
		}
		var buf [18]byte
		copy(buf[:16], ip16)
		binary.BigEndian.PutUint16(buf[16:], p.Port)
		compact = append(compact, buf[:]...)
		var f byte
		if p.Seed {
			f |= 0x02
		}
		flags = append(flags, f)
	}
	return compact, flags
}

func encodeDroppedV4(peers []PEXPeer) []byte {
	compact := make([]byte, 0, len(peers)*6)
	for _, p := range peers {
		ip4 := p.IP.To4()
		if ip4 == nil {
			continue
		}
		var buf [6]byte
		copy(buf[:4], ip4)
		binary.BigEndian.PutUint16(buf[4:], p.Port)
		compact = append(compact, buf[:]...)
	}
	return compact
}

func encodeDroppedV6(peers []PEXPeer) []byte {
	compact := make([]byte, 0, len(peers)*18)
	for _, p := range peers {
		if p.IP.To4() != nil {
			continue
		}
		ip16 := p.IP.To16()
		if ip16 == nil {
			continue
		}
		var buf [18]byte
		copy(buf[:16], ip16)
		binary.BigEndian.PutUint16(buf[16:], p.Port)
		compact = append(compact, buf[:]...)
	}
	return compact
}

// DecodePEX decodes a bencoded PEX message payload.
// Parses both IPv4 (added/dropped) and IPv6 (added6/dropped6) fields.
func DecodePEX(data []byte) (*PEXMessage, error) {
	decoded, err := bencode.Decode(data)
	if err != nil {
		return nil, err
	}
	d, ok := decoded.(map[string]any)
	if !ok {
		return nil, nil
	}

	msg := &PEXMessage{}

	if added, ok := d["added"].(string); ok && len(added)%6 == 0 {
		flags, _ := d["added.f"].(string)
		n := len(added) / 6
		if n > maxPEXPeers {
			return nil, fmt.Errorf("peer: PEX added too many peers: %d", n)
		}
		for i := range n {
			off := i * 6
			ip := make(net.IP, 4)
			copy(ip, added[off:off+4])
			port := binary.BigEndian.Uint16([]byte(added[off+4 : off+6]))
			p := PEXPeer{IP: ip, Port: port}
			if i < len(flags) && flags[i]&0x02 != 0 {
				p.Seed = true
			}
			msg.Added = append(msg.Added, p)
		}
	}

	if added6, ok := d["added6"].(string); ok && len(added6)%18 == 0 {
		flags, _ := d["added6.f"].(string)
		n := len(added6) / 18
		if n > maxPEXPeers {
			return nil, fmt.Errorf("peer: PEX added6 too many peers: %d", n)
		}
		for i := range n {
			off := i * 18
			ip := make(net.IP, 16)
			copy(ip, added6[off:off+16])
			port := binary.BigEndian.Uint16([]byte(added6[off+16 : off+18]))
			p := PEXPeer{IP: ip, Port: port}
			if i < len(flags) && flags[i]&0x02 != 0 {
				p.Seed = true
			}
			msg.Added = append(msg.Added, p)
		}
	}

	if dropped, ok := d["dropped"].(string); ok && len(dropped)%6 == 0 {
		n := len(dropped) / 6
		if n > maxPEXPeers {
			return nil, fmt.Errorf("peer: PEX dropped too many peers: %d", n)
		}
		for i := range n {
			off := i * 6
			ip := make(net.IP, 4)
			copy(ip, dropped[off:off+4])
			port := binary.BigEndian.Uint16([]byte(dropped[off+4 : off+6]))
			msg.Dropped = append(msg.Dropped, PEXPeer{IP: ip, Port: port})
		}
	}

	if dropped6, ok := d["dropped6"].(string); ok && len(dropped6)%18 == 0 {
		n := len(dropped6) / 18
		if n > maxPEXPeers {
			return nil, fmt.Errorf("peer: PEX dropped6 too many peers: %d", n)
		}
		for i := range n {
			off := i * 18
			ip := make(net.IP, 16)
			copy(ip, dropped6[off:off+16])
			port := binary.BigEndian.Uint16([]byte(dropped6[off+16 : off+18]))
			msg.Dropped = append(msg.Dropped, PEXPeer{IP: ip, Port: port})
		}
	}

	return msg, nil
}
