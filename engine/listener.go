package engine

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/skmtkytr/stor/peer"
)

// PeerListener accepts incoming TCP connections from peers
// and routes them to the appropriate session by info hash.
type PeerListener struct {
	ln       net.Listener
	mu       sync.RWMutex
	handlers map[[20]byte]IncomingPeerHandler
	closed   chan struct{}
}

// IncomingPeerHandler handles an incoming peer connection for a specific torrent.
type IncomingPeerHandler interface {
	HandleIncoming(conn net.Conn, peerHS *peer.Handshake)
}

// NewPeerListener creates a listener on the given address (e.g. ":6881").
func NewPeerListener(addr string) (*PeerListener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listener: %w", err)
	}
	return &PeerListener{
		ln:       ln,
		handlers: make(map[[20]byte]IncomingPeerHandler),
		closed:   make(chan struct{}),
	}, nil
}

// Addr returns the listener's address.
func (pl *PeerListener) Addr() net.Addr {
	return pl.ln.Addr()
}

// Register adds a handler for a specific info hash.
func (pl *PeerListener) Register(infoHash [20]byte, h IncomingPeerHandler) {
	pl.mu.Lock()
	pl.handlers[infoHash] = h
	pl.mu.Unlock()
}

// Unregister removes the handler for an info hash.
func (pl *PeerListener) Unregister(infoHash [20]byte) {
	pl.mu.Lock()
	delete(pl.handlers, infoHash)
	pl.mu.Unlock()
}

// Run accepts connections in a loop. Blocks until Close is called.
func (pl *PeerListener) Run() {
	for {
		conn, err := pl.ln.Accept()
		if err != nil {
			select {
			case <-pl.closed:
				return // intentional close
			default:
				slog.Debug("listener: accept error", "error", err)
				continue
			}
		}
		go pl.handleConn(conn)
	}
}

// Close shuts down the listener.
func (pl *PeerListener) Close() error {
	close(pl.closed)
	return pl.ln.Close()
}

func (pl *PeerListener) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	// Read the incoming handshake to get info hash
	hs, err := peer.ReadHandshake(conn)
	if err != nil {
		slog.Debug("listener: handshake read failed", "remote", conn.RemoteAddr(), "error", err)
		return
	}

	pl.mu.RLock()
	handler, ok := pl.handlers[hs.InfoHash]
	pl.mu.RUnlock()

	if !ok {
		slog.Debug("listener: no handler for info hash",
			"remote", conn.RemoteAddr(),
			"info_hash", hex.EncodeToString(hs.InfoHash[:]))
		return
	}

	handler.HandleIncoming(conn, hs)
}
