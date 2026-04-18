package peer

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/skmtkytr/stor/bencode"
)

func TestPEXEncodeDecodeIPv6(t *testing.T) {
	msg := &PEXMessage{
		Added: []PEXPeer{
			{IP: net.ParseIP("2001:db8::1"), Port: 6881, Seed: false},
			{IP: net.ParseIP("::1"), Port: 51413, Seed: true},
			{IP: net.IPv4(1, 2, 3, 4), Port: 6881, Seed: false}, // v4 still works
		},
		Dropped: []PEXPeer{
			{IP: net.ParseIP("fe80::1"), Port: 8080},
		},
	}
	data, err := EncodePEX(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	got, err := DecodePEX(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got == nil {
		t.Fatal("decoded nil message")
	}

	if len(got.Added) != 3 {
		t.Fatalf("expected 3 added peers (2 v6 + 1 v4), got %d", len(got.Added))
	}
	if len(got.Dropped) != 1 {
		t.Fatalf("expected 1 dropped peer, got %d", len(got.Dropped))
	}

	// Verify IPv6 peers decoded correctly
	var foundV6_1, foundV6_2, foundV4, foundSeed bool
	for _, p := range got.Added {
		if p.IP.Equal(net.ParseIP("2001:db8::1")) && p.Port == 6881 {
			foundV6_1 = true
		}
		if p.IP.Equal(net.ParseIP("::1")) && p.Port == 51413 && p.Seed {
			foundV6_2 = true
			foundSeed = true
		}
		if p.IP.Equal(net.IPv4(1, 2, 3, 4)) && p.Port == 6881 {
			foundV4 = true
		}
	}
	if !foundV6_1 {
		t.Error("IPv6 peer 2001:db8::1 not found in decoded added")
	}
	if !foundV6_2 {
		t.Error("IPv6 peer ::1 not found in decoded added")
	}
	if !foundV4 {
		t.Error("IPv4 peer 1.2.3.4 not found in decoded added")
	}
	if !foundSeed {
		t.Error("seed flag lost for IPv6 peer ::1")
	}

	// Verify dropped v6
	if !got.Dropped[0].IP.Equal(net.ParseIP("fe80::1")) || got.Dropped[0].Port != 8080 {
		t.Errorf("dropped[0]: got %s:%d, want fe80::1:8080", got.Dropped[0].IP, got.Dropped[0].Port)
	}
}

func TestPEXDecodeMaxPeers6(t *testing.T) {
	// Build added6 with maxPEXPeers+1 peers (18 bytes each)
	n := maxPEXPeers + 1
	compact := make([]byte, n*18)
	for i := range n {
		// Set last byte of IP + port to make unique
		compact[i*18+15] = byte(i & 0xff)
		compact[i*18+16] = 0x1A
		compact[i*18+17] = 0xE1
	}
	flags := make([]byte, n)

	d := map[string]any{
		"added6":   string(compact),
		"added6.f": string(flags),
	}
	// We need to encode this raw so we can feed it to DecodePEX.
	// Using bencode package would require importing; build dict via EncodePEX would
	// trim at maxPEXPeers. Use bencode directly.
	// Inline bencode for dict with two string values:
	raw := buildBencodedPEXWithAdded6(compact, flags)

	_, err := DecodePEX(raw)
	if err == nil {
		t.Fatal("expected error for PEX added6 exceeding max")
	}
	_ = d
}

func TestEncodePEXOnlyV6(t *testing.T) {
	// When only IPv6 peers are given, the output dict must NOT contain the
	// v4 "added"/"dropped" fields — only added6/added6.f/dropped6.
	msg := &PEXMessage{
		Added: []PEXPeer{
			{IP: net.ParseIP("2001:db8::1"), Port: 6881},
		},
		Dropped: []PEXPeer{
			{IP: net.ParseIP("2001:db8::2"), Port: 8080},
		},
	}
	data, err := EncodePEX(msg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := bencode.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	d := decoded.(map[string]any)
	if _, has := d["added"]; has {
		t.Error("added field must not appear when no v4 peers present")
	}
	if _, has := d["added.f"]; has {
		t.Error("added.f field must not appear when no v4 peers present")
	}
	if _, has := d["dropped"]; has {
		t.Error("dropped field must not appear when no v4 peers present")
	}
	if _, has := d["added6"]; !has {
		t.Error("added6 field must be present")
	}
	if _, has := d["dropped6"]; !has {
		t.Error("dropped6 field must be present")
	}
}

func TestEncodePEXOnlyV4(t *testing.T) {
	// When only IPv4 peers are given, the output must NOT contain added6/dropped6.
	msg := &PEXMessage{
		Added: []PEXPeer{
			{IP: net.IPv4(1, 2, 3, 4), Port: 6881},
		},
		Dropped: []PEXPeer{
			{IP: net.IPv4(5, 6, 7, 8), Port: 8080},
		},
	}
	data, err := EncodePEX(msg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := bencode.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	d := decoded.(map[string]any)
	if _, has := d["added6"]; has {
		t.Error("added6 field must not appear when no v6 peers present")
	}
	if _, has := d["added6.f"]; has {
		t.Error("added6.f field must not appear when no v6 peers present")
	}
	if _, has := d["dropped6"]; has {
		t.Error("dropped6 field must not appear when no v6 peers present")
	}
	if _, has := d["added"]; !has {
		t.Error("added field must be present")
	}
}

func TestDecodePEXDropped6Limit(t *testing.T) {
	// dropped6 exceeding maxPEXPeers must be rejected (parallel to added6).
	n := maxPEXPeers + 1
	compact := make([]byte, n*18)
	raw := buildBencodedPEXWithDropped6(compact)
	_, err := DecodePEX(raw)
	if err == nil {
		t.Fatal("expected error for PEX dropped6 exceeding max")
	}
}

func TestDecodePEXInvalidAdded6Length(t *testing.T) {
	// added6 with length not multiple of 18 should be silently ignored
	// (not crash, not error) for forward compatibility.
	raw := buildBencodedPEXWithAdded6([]byte("bad"), []byte{})
	msg, err := DecodePEX(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil && len(msg.Added) > 0 {
		t.Errorf("expected no peers from malformed added6, got %d", len(msg.Added))
	}
}

func TestEncodePEXSkipsNilIP(t *testing.T) {
	// Defensive: a Peer with nil IP (malformed input) must be silently skipped
	// rather than panicking on To4()/To16() calls.
	msg := &PEXMessage{
		Added: []PEXPeer{
			{IP: nil, Port: 6881},
			{IP: net.IPv4(1, 2, 3, 4), Port: 6881},
		},
		Dropped: []PEXPeer{
			{IP: nil, Port: 8080},
		},
	}
	data, err := EncodePEX(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodePEX(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Added) != 1 {
		t.Errorf("expected 1 added peer (nil skipped), got %d", len(got.Added))
	}
	if len(got.Dropped) != 0 {
		t.Errorf("expected 0 dropped peers (nil skipped), got %d", len(got.Dropped))
	}
}

func TestDecodePEXFlagsShorterThanAdded6(t *testing.T) {
	// Protocol robustness: if the peer sends added6 with 3 peers but only 1
	// flag byte, the decode must not panic on out-of-bounds flags access.
	// The 2nd and 3rd peers default to Seed=false.
	n := 3
	compact := make([]byte, n*18)
	for i := range n {
		compact[i*18+15] = byte(i + 1)
		binary.BigEndian.PutUint16(compact[i*18+16:], 6881)
	}
	flags := []byte{0x02} // only 1 byte for 3 peers

	raw := buildBencodedPEXWithAdded6(compact, flags)
	msg, err := DecodePEX(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(msg.Added) != 3 {
		t.Fatalf("expected 3 peers, got %d", len(msg.Added))
	}
	if !msg.Added[0].Seed {
		t.Error("peer[0] should have Seed=true (flag byte 0x02)")
	}
	if msg.Added[1].Seed || msg.Added[2].Seed {
		t.Error("peers 1 and 2 should have Seed=false (flags missing)")
	}
}

func buildBencodedPEXWithDropped6(dropped6 []byte) []byte {
	var buf []byte
	buf = append(buf, 'd')
	buf = append(buf, "8:dropped6"...)
	buf = appendBencodedStr(buf, dropped6)
	buf = append(buf, 'e')
	return buf
}

// buildBencodedPEXWithAdded6 creates a minimal bencode dict for test input.
func buildBencodedPEXWithAdded6(added6, flags []byte) []byte {
	// dict: d 6:added6 <len>:<bytes> 8:added6.f <len>:<bytes> e
	// Keys must be sorted: "added6" < "added6.f"
	var buf []byte
	buf = append(buf, 'd')
	buf = append(buf, "6:added6"...)
	buf = appendBencodedStr(buf, added6)
	buf = append(buf, "8:added6.f"...)
	buf = appendBencodedStr(buf, flags)
	buf = append(buf, 'e')
	return buf
}

func appendBencodedStr(buf, s []byte) []byte {
	lenStr := []byte{}
	n := len(s)
	if n == 0 {
		lenStr = append(lenStr, '0')
	} else {
		tmp := n
		var digits []byte
		for tmp > 0 {
			digits = append([]byte{byte('0' + tmp%10)}, digits...)
			tmp /= 10
		}
		lenStr = digits
	}
	buf = append(buf, lenStr...)
	buf = append(buf, ':')
	buf = append(buf, s...)
	return buf
}
