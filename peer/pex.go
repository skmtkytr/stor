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
func EncodePEX(msg *PEXMessage) ([]byte, error) {
	d := map[string]any{}

	if len(msg.Added) > 0 {
		compact := make([]byte, 0, len(msg.Added)*6)
		flags := make([]byte, 0, len(msg.Added))
		for _, p := range msg.Added {
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
		d["added"] = string(compact)
		d["added.f"] = string(flags)
	}

	if len(msg.Dropped) > 0 {
		compact := make([]byte, 0, len(msg.Dropped)*6)
		for _, p := range msg.Dropped {
			ip4 := p.IP.To4()
			if ip4 == nil {
				continue
			}
			var buf [6]byte
			copy(buf[:4], ip4)
			binary.BigEndian.PutUint16(buf[4:], p.Port)
			compact = append(compact, buf[:]...)
		}
		d["dropped"] = string(compact)
	}

	return bencode.Encode(d)
}

// DecodePEX decodes a bencoded PEX message payload.
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

	return msg, nil
}
