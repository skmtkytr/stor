package tracker

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"net/url"
	"time"
)

const (
	udpConnectMagic = 0x41727101980
	actionConnect   = 0
	actionAnnounce  = 1
	actionError     = 3
)

// AnnounceUDP sends a UDP tracker announce and returns the response.
func AnnounceUDP(req AnnounceRequest) (*AnnounceResponse, error) {
	u, err := url.Parse(req.AnnounceURL)
	if err != nil {
		return nil, fmt.Errorf("tracker: invalid UDP URL: %w", err)
	}

	addr := u.Host
	conn, err := net.DialTimeout("udp", addr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("tracker: UDP dial failed: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Connect
	connID, err := udpConnect(conn)
	if err != nil {
		return nil, err
	}

	// Announce
	return udpAnnounce(conn, connID, req)
}

func udpConnect(conn net.Conn) (uint64, error) {
	txnID := randomTxnID()

	// Send connect request (16 bytes)
	buf := make([]byte, 16)
	binary.BigEndian.PutUint64(buf[0:8], udpConnectMagic)
	binary.BigEndian.PutUint32(buf[8:12], actionConnect)
	binary.BigEndian.PutUint32(buf[12:16], txnID)

	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := conn.Write(buf); err != nil {
		return 0, fmt.Errorf("tracker: UDP connect write failed: %w", err)
	}

	// Read connect response (16 bytes)
	resp := make([]byte, 16)
	n, err := conn.Read(resp)
	if err != nil {
		return 0, fmt.Errorf("tracker: UDP connect read failed: %w", err)
	}
	if n < 16 {
		return 0, fmt.Errorf("tracker: UDP connect response too short: %d bytes", n)
	}

	action := binary.BigEndian.Uint32(resp[0:4])
	respTxnID := binary.BigEndian.Uint32(resp[4:8])
	connID := binary.BigEndian.Uint64(resp[8:16])

	if respTxnID != txnID {
		return 0, fmt.Errorf("tracker: transaction ID mismatch: got %d, want %d", respTxnID, txnID)
	}
	if action == actionError {
		return 0, fmt.Errorf("tracker: UDP connect error")
	}
	if action != actionConnect {
		return 0, fmt.Errorf("tracker: unexpected action %d in connect response", action)
	}

	return connID, nil
}

func udpAnnounce(conn net.Conn, connID uint64, req AnnounceRequest) (*AnnounceResponse, error) {
	txnID := randomTxnID()

	// Build announce request (98 bytes)
	buf := make([]byte, 98)
	binary.BigEndian.PutUint64(buf[0:8], connID)
	binary.BigEndian.PutUint32(buf[8:12], actionAnnounce)
	binary.BigEndian.PutUint32(buf[12:16], txnID)
	copy(buf[16:36], req.InfoHash[:])
	copy(buf[36:56], req.PeerID[:])
	binary.BigEndian.PutUint64(buf[56:64], uint64(req.Downloaded))
	binary.BigEndian.PutUint64(buf[64:72], uint64(req.Left))
	binary.BigEndian.PutUint64(buf[72:80], uint64(req.Uploaded))

	// Event: 0=none, 1=completed, 2=started, 3=stopped
	var event uint32
	switch req.Event {
	case EventCompleted:
		event = 1
	case EventStarted:
		event = 2
	case EventStopped:
		event = 3
	}
	binary.BigEndian.PutUint32(buf[80:84], event)

	binary.BigEndian.PutUint32(buf[84:88], 0)          // IP (default)
	binary.BigEndian.PutUint32(buf[88:92], 0)          // key
	binary.BigEndian.PutUint32(buf[92:96], 0xFFFFFFFF) // num_want (-1 = default)
	binary.BigEndian.PutUint16(buf[96:98], req.Port)

	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := conn.Write(buf); err != nil {
		return nil, fmt.Errorf("tracker: UDP announce write failed: %w", err)
	}

	// Read announce response (20+ bytes)
	resp := make([]byte, 8192)
	n, err := conn.Read(resp)
	if err != nil {
		return nil, fmt.Errorf("tracker: UDP announce read failed: %w", err)
	}
	if n < 20 {
		return nil, fmt.Errorf("tracker: UDP announce response too short: %d bytes", n)
	}
	resp = resp[:n]

	action := binary.BigEndian.Uint32(resp[0:4])
	respTxnID := binary.BigEndian.Uint32(resp[4:8])

	if respTxnID != txnID {
		return nil, fmt.Errorf("tracker: transaction ID mismatch")
	}
	if action == actionError {
		errMsg := ""
		if n > 8 {
			errMsg = string(resp[8:])
		}
		return nil, fmt.Errorf("tracker: UDP announce error: %s", errMsg)
	}
	if action != actionAnnounce {
		return nil, fmt.Errorf("tracker: unexpected action %d in announce response", action)
	}

	interval := binary.BigEndian.Uint32(resp[8:12])
	// Parse peers (6 bytes each: 4-byte IP + 2-byte port, starting at offset 20)
	peerData := resp[20:]
	if len(peerData)%6 != 0 {
		peerData = peerData[:len(peerData)/6*6] // truncate to multiple of 6
	}

	peers, err := parseCompactPeers(string(peerData))
	if err != nil {
		return nil, err
	}

	return &AnnounceResponse{
		Interval: int64(interval),
		Peers:    peers,
	}, nil
}

func randomTxnID() uint32 {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return binary.BigEndian.Uint32(buf[:])
}
