package download

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/storage"
	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/utp"
)

// Uploader serves piece data to incoming peers.
type Uploader struct {
	tf       *torrent.TorrentFile
	filePath string
	peerID   [20]byte
	bitfield peer.Bitfield
	pm       *PeerManager

	mu        sync.Mutex
	clients   []*Client
	stopped   bool
	runCancel context.CancelFunc
	serveWg   sync.WaitGroup // tracks in-flight HandleIncoming goroutines
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

// Run starts the upload choking algorithm. Blocks until ctx is cancelled
// or Stop is called.
func (u *Uploader) Run(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	u.mu.Lock()
	u.runCancel = cancel
	stopped := u.stopped
	u.mu.Unlock()
	if stopped {
		cancel()
		return
	}
	u.pm.Run(runCtx)
}

// Stop closes all connected client conns and blocks until every in-flight
// serve goroutine has fully exited (so the file descriptors they hold against
// filePath are released). Required before renaming filePath on filesystems
// that reject rename while files in the source tree are open (notably
// SMB/CIFS — Windows ERROR_SHARING_VIOLATION). Idempotent.
func (u *Uploader) Stop() {
	u.mu.Lock()
	if u.stopped {
		u.mu.Unlock()
		return
	}
	u.stopped = true
	cancel := u.runCancel
	clients := append([]*Client(nil), u.clients...)
	u.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, c := range clients {
		_ = c.conn.Close()
	}
	u.serveWg.Wait()
}

// SetPiece marks a piece as available for upload and notifies all connected
// peers via MsgHave so they know they can request it.
func (u *Uploader) SetPiece(index int) {
	u.mu.Lock()
	if u.bitfield != nil {
		u.bitfield.SetPiece(index)
	}
	clients := make([]*Client, len(u.clients))
	copy(clients, u.clients)
	u.mu.Unlock()

	// Broadcast MsgHave to all connected upload peers
	haveMsg := peer.NewHaveMessage(uint32(index))
	for _, c := range clients {
		_ = haveMsg.Write(c.w)
		_ = c.w.Flush()
	}
}

// HandleIncoming handles an incoming peer connection.
// The remote peer has already sent their handshake; conn is the raw TCP connection.
func (u *Uploader) HandleIncoming(conn net.Conn, remoteHS *peer.Handshake) {
	// Refuse new connections after Stop. The Add(1) below must happen under
	// the same lock as the stopped check, otherwise Stop's serveWg.Wait could
	// race with a late HandleIncoming and miss its goroutine.
	u.mu.Lock()
	if u.stopped {
		u.mu.Unlock()
		_ = conn.Close()
		return
	}
	u.serveWg.Add(1)
	u.mu.Unlock()
	defer u.serveWg.Done()

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

	_, isUTP := conn.(*utp.Conn)
	client := &Client{
		conn:         conn,
		r:            r,
		w:            w,
		peerID:       u.peerID,
		infoHash:     u.tf.InfoHash,
		Addr:         conn.RemoteAddr().String(),
		choking:      true, // start by choking them
		fastExt:      remoteHS.FastExtension,
		speedStart:   time.Now(),
		Incoming:     true,
		UsingUTP:     isUTP,
		RemotePeerID: remoteHS.PeerID,
		// Encrypted: not tracked for incoming — MSE handshake happens before
		// peer.ReadHandshake in the listener layer (future enhancement)
	}
	attachRateTargets(client)

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

	// nil bitfield means "have all" (BEP 6 convention)
	allHave := u.bitfield == nil
	if !allHave {
		allHave = true
		for i := range numPieces {
			if !u.bitfield.HasPiece(i) {
				allHave = false
				break
			}
		}
	}

	var initMsg *peer.Message
	if remoteHS.FastExtension && allHave {
		initMsg = &peer.Message{ID: peer.MsgHaveAll}
	} else if remoteHS.FastExtension && u.bitfield != nil && len(u.bitfield) == 0 {
		initMsg = &peer.Message{ID: peer.MsgHaveNone}
	} else if len(u.bitfield) > 0 {
		initMsg = &peer.Message{ID: peer.MsgBitfield, Payload: u.bitfield}
	}
	if initMsg != nil {
		if err := initMsg.Write(w); err != nil {
			client.SetDisconnectReason(classifyDownloadError(err))
			return
		}
		if err := w.Flush(); err != nil {
			client.SetDisconnectReason(classifyDownloadError(err))
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
			c.SetDisconnectReason("storage_open_failed")
			return
		}
		defer func() { _ = mw.Close() }()
		r = mw
	} else {
		f, err := os.Open(u.filePath)
		if err != nil {
			slog.Error("upload: open file failed", "path", u.filePath, "error", err)
			c.SetDisconnectReason("storage_open_failed")
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
			c.SetDisconnectReason(classifyDownloadError(err))
			return // peer disconnected
		}
		if msg == nil {
			continue // keep-alive
		}

		switch msg.ID {
		case peer.MsgInterested:
			// Immediately unchoke — don't make them wait for PeerManager rechoke
			slog.Debug("upload: peer interested, unchoking", "addr", c.Addr)
			_ = c.SendUnchoke()

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
				c.SetDisconnectReason("protocol_error: bad request: " + err.Error())
				return
			}

			// Validate request
			numPieces := uint32(len(u.tf.Info.PieceHashes))
			if index >= numPieces {
				slog.Debug("upload: invalid piece index", "addr", c.Addr, "index", index, "numPieces", numPieces)
				c.SetDisconnectReason("protocol_error: invalid piece index")
				return
			}
			if length > 32*1024 { // max 32KiB per block
				slog.Debug("upload: request too large", "addr", c.Addr, "length", length)
				c.SetDisconnectReason("protocol_error: request too large")
				return
			}

			// Read piece data from disk — safe: index < numPieces prevents overflow
			offset := int64(index)*pieceLen + int64(begin)
			if offset < 0 || offset+int64(length) > totalSize {
				slog.Debug("upload: request out of bounds", "addr", c.Addr)
				c.SetDisconnectReason("protocol_error: request out of bounds")
				return
			}

			block := make([]byte, length)
			n, err := r.ReadAt(block, offset)
			if err != nil || n != int(length) {
				slog.Debug("upload: read failed", "addr", c.Addr, "error", err)
				c.SetDisconnectReason("read_failed")
				return
			}

			// Send piece
			pieceMsg := peer.NewPieceMessage(index, begin, block)
			if err := pieceMsg.Write(c.w); err != nil {
				c.SetDisconnectReason(classifyDownloadError(err))
				return
			}
			if err := c.w.Flush(); err != nil {
				c.SetDisconnectReason(classifyDownloadError(err))
				return
			}

			c.uploaded.Add(int64(length))
			c.upCounter.Add(int64(length))

		case peer.MsgCancel:
			// Ignore cancel for now (we send immediately)

		case peer.MsgHave:
			idx, err := peer.ParseHave(msg.Payload)
			if err == nil {
				c.safeBitfieldSet(int(idx))
			}

		case peer.MsgBitfield:
			// First post-handshake bitfield. Without this, c.bitfield
			// stayed nil and Snapshot.Progress reported every incoming
			// peer as a 100% seeder — leeching peers showed up as 100%
			// even while we were uploading to them.
			c.bitfield = peer.Bitfield(msg.Payload)
			c.haveAll = false

		case peer.MsgHaveAll:
			c.haveAll = true

		case peer.MsgHaveNone:
			c.bitfield = peer.Bitfield{}
			c.haveAll = false
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
