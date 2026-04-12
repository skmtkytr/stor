package tracker

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/skmtkytr/stor/bencode"
)

// MaxTrackerResponseSize is the maximum HTTP response body size (10 MB).
const MaxTrackerResponseSize = 10 * 1024 * 1024

// MaxPeersPerResponse is the maximum number of peers accepted from a single
// tracker response to prevent memory exhaustion from malicious trackers.
const MaxPeersPerResponse = 10000

// Event represents the tracker announce event.
type Event string

const (
	EventNone      Event = ""
	EventStarted   Event = "started"
	EventCompleted Event = "completed"
	EventStopped   Event = "stopped"
)

// Peer represents a peer returned by the tracker.
type Peer struct {
	IP   net.IP
	Port uint16
}

// String returns "IP:port".
func (p Peer) String() string {
	return net.JoinHostPort(p.IP.String(), strconv.Itoa(int(p.Port)))
}

// AnnounceRequest contains the parameters for a tracker announce.
type AnnounceRequest struct {
	AnnounceURL string
	InfoHash    [20]byte
	PeerID      [20]byte
	Port        uint16
	Uploaded    int64
	Downloaded  int64
	Left        int64
	Event       Event
	NumWant     int // peers requested; 0 means use default (200)
}

// AnnounceResponse contains the tracker's response.
type AnnounceResponse struct {
	Interval int64
	Peers    []Peer
}

// Announce sends a tracker announce, auto-detecting HTTP or UDP protocol.
func Announce(req AnnounceRequest) (*AnnounceResponse, error) {
	if strings.HasPrefix(req.AnnounceURL, "udp://") {
		return AnnounceUDP(req)
	}
	return AnnounceHTTP(req)
}

// AnnounceHTTP sends an HTTP announce request and returns the response.
// Uses DNS pinning to prevent DNS rebinding SSRF attacks.
func AnnounceHTTP(req AnnounceRequest) (*AnnounceResponse, error) {
	return announceHTTP(req, nil)
}

// announceHTTP is the internal implementation. If client is nil, a DNS-pinning
// client is created that validates the tracker host is not private.
func announceHTTP(req AnnounceRequest, client *http.Client) (*AnnounceResponse, error) {
	u, err := buildAnnounceURL(req)
	if err != nil {
		return nil, err
	}

	if client == nil {
		// DNS pinning: resolve hostname, validate IP, connect to resolved IP directly.
		host := u.Hostname()
		resolvedIP, resolveErr := resolveAndValidateTrackerHost(host)
		if resolveErr != nil {
			return nil, fmt.Errorf("tracker: %w", resolveErr)
		}

		port := u.Port()
		if port == "" {
			if u.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		pinnedAddr := net.JoinHostPort(resolvedIP, port)

		client = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, pinnedAddr)
				},
			},
		}
	}

	resp, err := client.Get(u.String())
	if err != nil {
		return nil, fmt.Errorf("tracker: HTTP request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxTrackerResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("tracker: failed to read response: %w", err)
	}

	if int64(len(body)) > MaxTrackerResponseSize {
		return nil, fmt.Errorf("tracker: response too large: %d bytes (limit %d)", len(body), MaxTrackerResponseSize)
	}

	return parseAnnounceResponse(body)
}

func buildAnnounceURL(req AnnounceRequest) (*url.URL, error) {
	u, err := url.Parse(req.AnnounceURL)
	if err != nil {
		return nil, fmt.Errorf("tracker: invalid announce URL: %w", err)
	}

	q := u.Query()
	// info_hash and peer_id are raw bytes, URL-encoded
	q.Set("info_hash", string(req.InfoHash[:]))
	q.Set("peer_id", string(req.PeerID[:]))
	q.Set("port", strconv.Itoa(int(req.Port)))
	q.Set("uploaded", strconv.FormatInt(req.Uploaded, 10))
	q.Set("downloaded", strconv.FormatInt(req.Downloaded, 10))
	q.Set("left", strconv.FormatInt(req.Left, 10))
	q.Set("compact", "1")
	numWant := req.NumWant
	if numWant <= 0 {
		numWant = 200
	}
	q.Set("numwant", strconv.Itoa(numWant))

	if req.Event != EventNone {
		q.Set("event", string(req.Event))
	}

	u.RawQuery = q.Encode()
	return u, nil
}

func parseAnnounceResponse(data []byte) (*AnnounceResponse, error) {
	decoded, err := bencode.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("tracker: failed to decode response: %w", err)
	}

	d, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("tracker: response is not a dict")
	}

	// Check for failure
	if reason, ok := d["failure reason"].(string); ok {
		return nil, fmt.Errorf("tracker: %s", reason)
	}

	resp := &AnnounceResponse{}

	if interval, ok := d["interval"].(int64); ok {
		resp.Interval = interval
	}

	// Parse peers: compact (string) or dict list
	switch peers := d["peers"].(type) {
	case string:
		resp.Peers, err = parseCompactPeers(peers)
		if err != nil {
			return nil, err
		}
	case []any:
		resp.Peers, err = parseDictPeers(peers)
		if err != nil {
			return nil, err
		}
	}

	return resp, nil
}

// parseCompactPeers parses BEP 23 compact peer list.
// Each peer is 6 bytes: 4-byte IPv4 + 2-byte port (big-endian).
func parseCompactPeers(data string) ([]Peer, error) {
	if len(data)%6 != 0 {
		return nil, fmt.Errorf("tracker: compact peers length %d is not a multiple of 6", len(data))
	}

	numPeers := len(data) / 6
	if numPeers > MaxPeersPerResponse {
		numPeers = MaxPeersPerResponse
	}
	peers := make([]Peer, numPeers)
	for i := range numPeers {
		offset := i * 6
		ip := net.IP(make([]byte, 4))
		copy(ip, data[offset:offset+4])
		port := binary.BigEndian.Uint16([]byte(data[offset+4 : offset+6]))
		peers[i] = Peer{IP: ip, Port: port}
	}
	return peers, nil
}

// resolveAndValidateTrackerHost resolves a hostname and validates it is not
// a private/loopback/link-local address. This prevents DNS rebinding SSRF.
func resolveAndValidateTrackerHost(host string) (string, error) {
	if host == "localhost" {
		return "", fmt.Errorf("host %q is localhost", host)
	}

	// If already an IP, validate directly
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return "", fmt.Errorf("host %s is a private/reserved address", host)
		}
		return ip.String(), nil
	}

	// Resolve hostname
	addrs, err := net.LookupHost(host)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return "", fmt.Errorf("host %s resolves to private address %s", host, addr)
		}
		return ip.String(), nil
	}
	return "", fmt.Errorf("host %s has no valid public addresses", host)
}

// FilterPrivatePeers removes peers with private/loopback/link-local addresses.
// This prevents SSRF and internal network scanning from malicious tracker responses.
func FilterPrivatePeers(peers []Peer) []Peer {
	result := make([]Peer, 0, len(peers))
	for _, p := range peers {
		if p.IP == nil {
			continue
		}
		if p.IP.IsLoopback() || p.IP.IsPrivate() || p.IP.IsLinkLocalUnicast() || p.IP.IsLinkLocalMulticast() || p.IP.IsUnspecified() {
			continue
		}
		result = append(result, p)
	}
	return result
}

// parseDictPeers parses the legacy (non-compact) peer list.
func parseDictPeers(list []any) ([]Peer, error) {
	cap := len(list)
	if cap > MaxPeersPerResponse {
		cap = MaxPeersPerResponse
	}
	peers := make([]Peer, 0, cap)
	for _, item := range list {
		if len(peers) >= MaxPeersPerResponse {
			break
		}
		d, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("tracker: peer entry is not a dict")
		}

		ipStr, ok := d["ip"].(string)
		if !ok {
			return nil, errors.New("tracker: peer missing 'ip'")
		}
		ip := net.ParseIP(ipStr)
		if ip == nil {
			return nil, fmt.Errorf("tracker: invalid peer IP %q", ipStr)
		}

		portVal, ok := d["port"].(int64)
		if !ok || portVal <= 0 || portVal > 65535 {
			return nil, fmt.Errorf("tracker: invalid peer port: %v", d["port"])
		}

		peers = append(peers, Peer{IP: ip, Port: uint16(portVal)})
	}
	return peers, nil
}
