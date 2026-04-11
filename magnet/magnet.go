package magnet

import (
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// Magnet represents a parsed magnet URI.
type Magnet struct {
	InfoHash    [20]byte
	InfoHashV2  [32]byte // BEP 52: SHA-256 info hash (zero if v1-only)
	HasV2       bool     // true if urn:btmh: was present
	DisplayName string
	Trackers    []string
	Peers       []string // x.pe values (host:port)
}

// Parse parses a magnet URI string.
func Parse(uri string) (*Magnet, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("magnet: invalid URI: %w", err)
	}
	if u.Scheme != "magnet" {
		return nil, fmt.Errorf("magnet: expected 'magnet' scheme, got %q", u.Scheme)
	}

	params := u.Query()

	m := &Magnet{}

	// xt (exact topic) — required; may have multiple values for hybrid
	xts := params["xt"]
	if len(xts) == 0 {
		return nil, fmt.Errorf("magnet: missing 'xt' parameter")
	}

	for _, xt := range xts {
		if strings.HasPrefix(xt, "urn:btih:") {
			hash, err := parseInfoHashV1(xt)
			if err != nil {
				return nil, err
			}
			m.InfoHash = hash
		} else if strings.HasPrefix(xt, "urn:btmh:") {
			hash, err := parseInfoHashV2(xt)
			if err != nil {
				return nil, err
			}
			m.InfoHashV2 = hash
			m.HasV2 = true
		}
	}

	// Must have at least v1 info hash (v2-only magnets not supported yet)
	var zero [20]byte
	if m.InfoHash == zero {
		if m.HasV2 {
			return nil, fmt.Errorf("magnet: v2-only magnets not yet supported (no urn:btih:)")
		}
		return nil, fmt.Errorf("magnet: missing 'xt' parameter with urn:btih:")
	}

	// dn (display name) — optional
	m.DisplayName = params.Get("dn")

	// tr (tracker) — optional, multiple allowed
	m.Trackers = params["tr"]

	// x.pe (peer) — optional, multiple allowed
	m.Peers = params["x.pe"]

	return m, nil
}

func parseInfoHashV1(xt string) ([20]byte, error) {
	var hash [20]byte
	raw := xt[len("urn:btih:"):]

	switch len(raw) {
	case 40:
		decoded, err := hex.DecodeString(raw)
		if err != nil {
			return hash, fmt.Errorf("magnet: invalid hex info hash: %w", err)
		}
		copy(hash[:], decoded)
	case 32:
		decoded, err := base32.StdEncoding.DecodeString(strings.ToUpper(raw))
		if err != nil {
			return hash, fmt.Errorf("magnet: invalid base32 info hash: %w", err)
		}
		copy(hash[:], decoded)
	default:
		return hash, fmt.Errorf("magnet: info hash has unexpected length %d (want 40 hex or 32 base32)", len(raw))
	}
	return hash, nil
}

// parseInfoHashV2 parses a BEP 52 v2 info hash from urn:btmh:1220<hex>.
// The multihash prefix 1220 means SHA-256 (0x12) with 32-byte digest (0x20).
func parseInfoHashV2(xt string) ([32]byte, error) {
	var hash [32]byte
	raw := xt[len("urn:btmh:"):]

	// Expect multihash: 1220 + 64 hex chars
	if !strings.HasPrefix(raw, "1220") {
		return hash, fmt.Errorf("magnet: unsupported multihash prefix in %q (expected 1220)", xt)
	}
	hexStr := raw[4:]
	if len(hexStr) != 64 {
		return hash, fmt.Errorf("magnet: v2 info hash has unexpected length %d (want 64 hex)", len(hexStr))
	}
	decoded, err := hex.DecodeString(hexStr)
	if err != nil {
		return hash, fmt.Errorf("magnet: invalid hex v2 info hash: %w", err)
	}
	copy(hash[:], decoded)
	return hash, nil
}
