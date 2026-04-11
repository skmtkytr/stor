package download

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"os"
	"sync"

	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/storage"
	"github.com/skmtkytr/stor/torrent"
)

// Uploader serves piece data to incoming peers.
type Uploader struct {
	tf       *torrent.TorrentFile
	filePath string
	peerID   [20]byte
	bitfield peer.Bitfield
	pm       *PeerManager

	mu      sync.Mutex
	clients []*Client
}

// NewUploader creates an uploader for a completed torrent.
func NewUploader(tf *torrent.TorrentFile, filePath string, peerID [20]byte, bitfield peer.Bitfield) *Uploader {
	return &Uploader{
		tf:       tf,
		filePath: filePath,
		peerID:   peerID,
		bitfield: bitfield,
		pm:       NewPeerManager(DefaultUnchokeSlots),
	}
}

// Run starts the upload choking algorithm. Blocks until ctx is cancelled.
func (u *Uploader) Run(ctx context.Context) {
	u.pm.Run(ctx)
}

// SetPiece marks a piece as available for upload (called during download).
func (u *Uploader) SetPiece(index int) {
	u.mu.Lock()
	u.bitfield.SetPiece(index)
	u.mu.Unlock()
}

// HandleIncoming handles an incoming peer connection.
// The remote peer has already sent their handshake; conn is the raw TCP connection.
func (u *Uploader) HandleIncoming(conn net.Conn, remoteHS *peer.Handshake) {
	// Complete handshake: send our side
	ourHS := &peer.Handshake{
		InfoHash:      u.tf.InfoHash,
		PeerID:        u.peerID,
		FastExtension: true,
	}
	if err := peer.WriteHandshake(conn, ourHS); err != nil {
		slog.Debug("upload: handshake write failed", "remote", conn.RemoteAddr(), "error", err)
		return
	}

	r := bufio.NewReaderSize(conn, 64*1024)
	w := bufio.NewWriterSize(conn, 32*1024)

	client := &Client{
		conn:     conn,
		r:        r,
		w:        w,
		peerID:   u.peerID,
		infoHash: u.tf.InfoHash,
		Addr:     conn.RemoteAddr().String(),
		choking:  true, // start by choking them
		fastExt:  remoteHS.FastExtension,
	}

	u.pm.Register(client)
	defer u.pm.Unregister(client)

	u.mu.Lock()
	u.clients = append(u.clients, client)
	u.mu.Unlock()

	defer func() {
		u.mu.Lock()
		for i, c := range u.clients {
			if c == client {
				u.clients = append(u.clients[:i], u.clients[i+1:]...)
				break
			}
		}
		u.mu.Unlock()
		_ = conn.Close()
	}()

	// Send our bitfield (or have-all/have-none for BEP 6)
	numPieces := len(u.tf.Info.PieceHashes)
	allHave := true
	for i := range numPieces {
		if !u.bitfield.HasPiece(i) {
			allHave = false
			break
		}
	}

	var initMsg *peer.Message
	if remoteHS.FastExtension && allHave {
		initMsg = &peer.Message{ID: peer.MsgHaveAll}
	} else if remoteHS.FastExtension && numPieces > 0 && !u.bitfield.HasPiece(0) && len(u.bitfield) == 0 {
		initMsg = &peer.Message{ID: peer.MsgHaveNone}
	} else if len(u.bitfield) > 0 {
		initMsg = &peer.Message{ID: peer.MsgBitfield, Payload: u.bitfield}
	}
	if initMsg != nil {
		if err := initMsg.Write(w); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
	}

	// Message loop: handle incoming requests
	u.serveLoop(client)
}

func (u *Uploader) serveLoop(c *Client) {
	var r storage.ReadAtReader
	if storage.IsMultiFile(u.tf) {
		mw, err := storage.NewMultiFileWriter(u.filePath, u.tf)
		if err != nil {
			slog.Error("upload: open multi-file dir failed", "path", u.filePath, "error", err)
			return
		}
		defer func() { _ = mw.Close() }()
		r = mw
	} else {
		f, err := os.Open(u.filePath)
		if err != nil {
			slog.Error("upload: open file failed", "path", u.filePath, "error", err)
			return
		}
		defer func() { _ = f.Close() }()
		r = f
	}

	pieceLen := int64(u.tf.Info.PieceLength)
	totalSize := storage.TotalSize(u.tf)

	for {
		msg, err := peer.ReadMessage(c.r)
		if err != nil {
			return // peer disconnected
		}
		if msg == nil {
			continue // keep-alive
		}

		switch msg.ID {
		case peer.MsgInterested:
			// Peer wants data — PeerManager will decide when to unchoke
			slog.Debug("upload: peer interested", "addr", c.Addr)

		case peer.MsgNotInterested:
			slog.Debug("upload: peer not interested", "addr", c.Addr)

		case peer.MsgRequest:
			if c.IsChoking() {
				if c.fastExt {
					if idx, begin, length, err := peer.ParseRequest(msg.Payload); err == nil {
						rejectMsg := peer.NewRejectMessage(idx, begin, length)
						_ = rejectMsg.Write(c.w)
						_ = c.w.Flush()
					}
				}
				continue
			}

			index, begin, length, err := peer.ParseRequest(msg.Payload)
			if err != nil {
				slog.Debug("upload: bad request", "addr", c.Addr, "error", err)
				return
			}

			// Validate request
			if length > 32*1024 { // max 32KiB per block
				slog.Debug("upload: request too large", "addr", c.Addr, "length", length)
				return
			}

			// Read piece data from disk
			offset := int64(index)*pieceLen + int64(begin)
			if offset+int64(length) > totalSize {
				slog.Debug("upload: request out of bounds", "addr", c.Addr)
				return
			}

			block := make([]byte, length)
			n, err := r.ReadAt(block, offset)
			if err != nil || n != int(length) {
				slog.Debug("upload: read failed", "addr", c.Addr, "error", err)
				return
			}

			// Send piece
			pieceMsg := peer.NewPieceMessage(index, begin, block)
			if err := pieceMsg.Write(c.w); err != nil {
				return
			}
			if err := c.w.Flush(); err != nil {
				return
			}

			c.uploaded.Add(int64(length))

		case peer.MsgCancel:
			// Ignore cancel for now (we send immediately)

		case peer.MsgHave:
			idx, err := peer.ParseHave(msg.Payload)
			if err == nil {
				c.bitfield.SetPiece(int(idx))
			}
		}
	}
}

// PeerCount returns the number of connected upload peers.
func (u *Uploader) PeerCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.clients)
}

// TotalUploadSpeed returns the sum of upload speeds across all connected peers.
func (u *Uploader) TotalUploadSpeed() int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	var total float64
	for _, c := range u.clients {
		total += c.UploadSpeed()
	}
	return int64(total)
}
