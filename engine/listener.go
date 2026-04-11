package engine

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/utp"
)

// PeerListener accepts incoming TCP (and optionally uTP) connections
// from peers and routes them to the appropriate session by info hash.
type PeerListener struct {
	tcpLn    net.Listener
	utpLn    *utp.Listener // nil if uTP disabled
	mu       sync.RWMutex
	handlers map[[20]byte]IncomingPeerHandler
	closed   chan struct{}
}

// IncomingPeerHandler handles an incoming peer connection for a specific torrent.
type IncomingPeerHandler interface {
	HandleIncoming(conn net.Conn, peerHS *peer.Handshake)
}

// NewPeerListener creates a TCP listener on the given address (e.g. ":6881").
func NewPeerListener(addr string) (*PeerListener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listener: %w", err)
	}
	return &PeerListener{
		tcpLn:    ln,
		handlers: make(map[[20]byte]IncomingPeerHandler),
		closed:   make(chan struct{}),
	}, nil
}

// NewPeerListenerWithUTP creates TCP and uTP listeners on the given address.
func NewPeerListenerWithUTP(addr string) (*PeerListener, error) {
	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listener: tcp: %w", err)
	}
	utpLn, err := utp.Listen("udp", addr)
	if err != nil {
		_ = tcpLn.Close()
		return nil, fmt.Errorf("listener: utp: %w", err)
	}
	return &PeerListener{
		tcpLn:    tcpLn,
		utpLn:    utpLn,
		handlers: make(map[[20]byte]IncomingPeerHandler),
		closed:   make(chan struct{}),
	}, nil
}

// Addr returns the TCP listener's address.
func (pl *PeerListener) Addr() net.Addr {
	return pl.tcpLn.Addr()
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
	if pl.utpLn != nil {
		go pl.acceptLoop(pl.utpLn)
	}
	pl.acceptLoop(pl.tcpLn)
}

func (pl *PeerListener) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
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

// Close shuts down all listeners.
func (pl *PeerListener) Close() error {
	close(pl.closed)
	err := pl.tcpLn.Close()
	if pl.utpLn != nil {
		if utpErr := pl.utpLn.Close(); utpErr != nil && err == nil {
			err = utpErr
		}
	}
	return err
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
