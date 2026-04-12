package tracker

import (
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
func AnnounceHTTP(req AnnounceRequest) (*AnnounceResponse, error) {
	u, err := buildAnnounceURL(req)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 15 * time.Second}
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

// parseDictPeers parses the legacy (non-compact) peer list.
func parseDictPeers(list []any) ([]Peer, error) {
	peers := make([]Peer, 0, len(list))
	for _, item := range list {
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
		if !ok {
			return nil, errors.New("tracker: peer missing 'port'")
		}

		peers = append(peers, Peer{IP: ip, Port: uint16(portVal)})
	}
	return peers, nil
}
