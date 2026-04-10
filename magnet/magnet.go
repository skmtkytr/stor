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

	// xt (exact topic) — required
	xt := params.Get("xt")
	if xt == "" {
		return nil, fmt.Errorf("magnet: missing 'xt' parameter")
	}

	infoHash, err := parseInfoHash(xt)
	if err != nil {
		return nil, err
	}
	m.InfoHash = infoHash

	// dn (display name) — optional
	m.DisplayName = params.Get("dn")

	// tr (tracker) — optional, multiple allowed
	m.Trackers = params["tr"]

	// x.pe (peer) — optional, multiple allowed
	m.Peers = params["x.pe"]

	return m, nil
}

func parseInfoHash(xt string) ([20]byte, error) {
	var hash [20]byte

	prefix := "urn:btih:"
	if !strings.HasPrefix(xt, prefix) {
		return hash, fmt.Errorf("magnet: unsupported xt format: %q", xt)
	}

	raw := xt[len(prefix):]

	switch len(raw) {
	case 40:
		// Hex-encoded (40 chars = 20 bytes)
		decoded, err := hex.DecodeString(raw)
		if err != nil {
			return hash, fmt.Errorf("magnet: invalid hex info hash: %w", err)
		}
		copy(hash[:], decoded)
	case 32:
		// Base32-encoded (32 chars = 20 bytes)
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
