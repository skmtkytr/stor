package download

import (
	"bufio"
	"context"
	"crypto/sha1"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/skmtkytr/stor/mse"
	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/storage"
	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/tracker"
	"github.com/skmtkytr/stor/utp"
)

const (
	// BlockSize is the standard request block size (16 KiB).
	BlockSize = 16384

	// Defaults for configurable parameters.
	DefaultMaxPipeline = 16
	DefaultMaxPeers    = 100
	DefaultDialTimeout = 3 // seconds
)

// DownloadConfig holds tunable parameters for the download engine.
type DownloadConfig struct {
	MaxPeers    int  // max concurrent peer connections
	MaxPipeline int  // outstanding requests per peer
	DialTimeout int  // peer dial timeout in seconds
	Encryption  bool // attempt MSE/PE encryption (default: true)
	EnableUTP   bool // try uTP before TCP for peer connections
	DisablePEX  bool // BEP 27: do not advertise or use PEX
}

// DefaultDownloadConfig returns default download config.
func DefaultDownloadConfig() DownloadConfig {
	return DownloadConfig{
		MaxPeers:    DefaultMaxPeers,
		MaxPipeline: DefaultMaxPipeline,
		DialTimeout: DefaultDialTimeout,
		Encryption:  false, // enable explicitly via config
	}
}

// PieceResult contains a downloaded and verified piece.
type PieceResult struct {
	Index   int
	Data    []byte
	Release func() // returns buffer to pool; nil if not pooled
}

// Free returns the buffer to the pool if pooled. Safe to call when Release is nil.
func (r *PieceResult) Free() {
	if r.Release != nil {
		r.Release()
	}
}

// PieceWork describes a piece to download.
type PieceWork struct {
	Index  int
	Hash   [20]byte
	Length int
}

// piecePool returns a sync.Pool for piece buffers of a given size.
func newPiecePool(size int) *sync.Pool {
	return &sync.Pool{
		New: func() any {
			buf := make([]byte, size)
			return &buf
		},
	}
}

// Client represents a connection to a single peer with buffered I/O.
type Client struct {
	conn           net.Conn
	r              *bufio.Reader
	w              *bufio.Writer
	peerID         [20]byte
	infoHash       [20]byte
	bitfield       peer.Bitfield
	choked         bool // we are choked by peer
	choking        bool // we are choking peer
	sentInterested bool
	maxPipeline    int
	Addr           string // peer address for identification

	// Speed tracking (atomic: accessed from both worker and PeerManager goroutines)
	downloaded atomic.Int64
	uploaded   atomic.Int64
	speedStart time.Time
	lastSpeed  atomic.Int64 // bytes/sec * 1000 (fixed-point)

	// PEX (BEP 11)
	pexRemoteID uint8                 // remote's ut_pex message ID (0 = not supported)
	PeerSink    chan<- []tracker.Peer // discovered peers from PEX are sent here

	// Upload while downloading: callback to serve incoming requests
	OnRequest func(index, begin, length uint32) []byte

	// Piece buffer pool (set by runWorkers for buffer reuse)
	piecePool  *sync.Pool
	disablePEX bool // BEP 27: suppress PEX for private torrents
	fastExt    bool // BEP 6: peer supports fast extension
}

// Speed returns the current download speed in bytes/sec.
func (c *Client) Speed() float64 {
	elapsed := time.Since(c.speedStart).Seconds()
	if elapsed < 1 {
		return float64(c.lastSpeed.Load())
	}
	speed := int64(float64(c.downloaded.Load()) / elapsed)
	c.lastSpeed.Store(speed)
	return float64(speed)
}

// ResetSpeed resets the speed measurement window.
func (c *Client) ResetSpeed() {
	c.Speed() // update lastSpeed
	c.downloaded.Store(0)
	c.speedStart = time.Now()
}

// UploadSpeed returns bytes/sec uploaded to this peer.
func (c *Client) UploadSpeed() float64 {
	elapsed := time.Since(c.speedStart).Seconds()
	if elapsed < 1 {
		return 0
	}
	return float64(c.uploaded.Load()) / elapsed
}

// SendChoke sends a choke message to the peer.
func (c *Client) SendChoke() error {
	if c.choking {
		return nil
	}
	msg := &peer.Message{ID: peer.MsgChoke}
	if err := msg.Write(c.w); err != nil {
		return err
	}
	if err := c.w.Flush(); err != nil {
		return err
	}
	c.choking = true
	return nil
}

// SendUnchoke sends an unchoke message to the peer.
func (c *Client) SendUnchoke() error {
	if !c.choking {
		return nil
	}
	msg := &peer.Message{ID: peer.MsgUnchoke}
	if err := msg.Write(c.w); err != nil {
		return err
	}
	if err := c.w.Flush(); err != nil {
		return err
	}
	c.choking = false
	return nil
}

// IsChoking returns whether we are choking this peer.
func (c *Client) IsChoking() bool {
	return c.choking
}

// NewClient connects to a peer, performs the handshake, and receives the bitfield.
func NewClient(p tracker.Peer, infoHash, peerID [20]byte) (*Client, error) {
	return NewClientWithTimeout(p, infoHash, peerID, DefaultDialTimeout)
}

// NewClientWithTimeout connects to a peer with a configurable dial timeout.
func NewClientWithTimeout(p tracker.Peer, infoHash, peerID [20]byte, dialTimeoutSec int) (*Client, error) {
	return newClient(p, infoHash, peerID, dialTimeoutSec, false)
}

// NewClientEncrypted tries the preferred protocol first, falling back to the
// alternative after a short delay. The preferEncrypt hint (shared across
// workers) is updated based on which method succeeds, so subsequent calls
// try the winning protocol first.
func NewClientEncrypted(p tracker.Peer, infoHash, peerID [20]byte, dialTimeoutSec int, preferEncrypt *atomic.Bool, tryUTP bool, noPEX bool) (*Client, error) {
	preferred := preferEncrypt != nil && preferEncrypt.Load()

	// Try preferred method first
	c, err := newClientFull(p, infoHash, peerID, dialTimeoutSec, preferred, tryUTP, noPEX)
	if err == nil {
		return c, nil
	}

	// Preferred failed — try fallback
	c, err2 := newClientFull(p, infoHash, peerID, dialTimeoutSec, !preferred, tryUTP, noPEX)
	if err2 == nil {
		if preferEncrypt != nil {
			preferEncrypt.Store(!preferred)
		}
		return c, nil
	}
	return nil, err
}

// dialPeer connects to a peer via uTP (if enabled) or TCP.
func dialPeer(addr string, timeout time.Duration, tryUTP bool) (net.Conn, error) {
	if tryUTP {
		conn, err := utp.DialTimeout(addr, timeout)
		if err == nil {
			return conn, nil
		}
		// uTP failed, fall back to TCP
	}
	return net.DialTimeout("tcp", addr, timeout)
}

func newClient(p tracker.Peer, infoHash, peerID [20]byte, dialTimeoutSec int, encrypt bool) (*Client, error) {
	return newClientFull(p, infoHash, peerID, dialTimeoutSec, encrypt, false, false)
}

func newClientFull(p tracker.Peer, infoHash, peerID [20]byte, dialTimeoutSec int, encrypt bool, tryUTP bool, noPEX bool) (*Client, error) {
	rawConn, err := dialPeer(p.String(), time.Duration(dialTimeoutSec)*time.Second, tryUTP)
	if err != nil {
		return nil, fmt.Errorf("download: connect to %s failed: %w", p, err)
	}

	conn := net.Conn(rawConn)
	closeOnErr := func() { _ = rawConn.Close() }

	// MSE/PE encryption handshake
	if encrypt {
		encConn, _, mseErr := mse.HandshakeOutgoing(rawConn, infoHash, mse.CryptoRC4)
		if mseErr != nil {
			closeOnErr()
			return nil, fmt.Errorf("download: MSE handshake to %s failed: %w", p, mseErr)
		}
		conn = encConn
	}

	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	hs := &peer.Handshake{InfoHash: infoHash, PeerID: peerID, Extensions: true, FastExtension: true}
	if err := peer.WriteHandshake(conn, hs); err != nil {
		closeOnErr()
		return nil, fmt.Errorf("download: handshake write failed: %w", err)
	}

	resp, err := peer.ReadHandshake(conn)
	if err != nil {
		closeOnErr()
		return nil, fmt.Errorf("download: handshake read failed: %w", err)
	}
	if resp.InfoHash != infoHash {
		closeOnErr()
		return nil, fmt.Errorf("download: info hash mismatch")
	}

	c := &Client{
		conn:        conn,
		r:           bufio.NewReaderSize(conn, 64*1024),
		w:           bufio.NewWriterSize(conn, 32*1024),
		peerID:      peerID,
		infoHash:    infoHash,
		choked:      true,
		maxPipeline: DefaultMaxPipeline,
		Addr:        p.String(),
		speedStart:  time.Now(),
		disablePEX:  noPEX,
		fastExt:     resp.FastExtension,
	}

	// BEP 10: send extension handshake if peer supports extensions
	if resp.Extensions {
		m := map[string]int64{}
		if !c.disablePEX {
			m["ut_pex"] = int64(2)
		}
		extHS := &peer.ExtHandshake{
			M: m,
			V: "stor/0.1",
		}
		payload, _ := peer.EncodeExtHandshake(extHS)
		extMsg := peer.NewExtendedMessage(peer.ExtHandshakeID, payload)
		if err := extMsg.Write(c.w); err == nil {
			_ = c.w.Flush()
		}
	}

	// Read initial messages: bitfield and/or ext handshake response.
	// Some peers send ext handshake before bitfield, so read up to 3 messages.
	// Use a short deadline to avoid blocking if peer has nothing more to send.
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	for range 3 {
		msg, readErr := peer.ReadMessage(c.r)
		if readErr != nil {
			break
		}
		if msg == nil {
			continue // keep-alive
		}
		switch msg.ID {
		case peer.MsgBitfield:
			c.bitfield = peer.Bitfield(msg.Payload)
		case peer.MsgHaveAll:
			// BEP 6: peer has all pieces — set full bitfield later when numPieces is known
			c.bitfield = nil // sentinel: nil means "have all"
		case peer.MsgHaveNone:
			// BEP 6: peer has no pieces
			c.bitfield = peer.Bitfield{}
		case peer.MsgExtended:
			c.handleExtended(msg.Payload)
		case peer.MsgUnchoke:
			c.choked = false
		default:
			goto doneInit
		}
	}
doneInit:
	_ = conn.SetDeadline(time.Time{})

	return c, nil //nolint:nilerr // initial message read errors are not fatal
}

// handleExtended processes BEP 10 extended messages (handshake response, PEX).
func (c *Client) handleExtended(payload []byte) {
	extID, data, err := peer.ParseExtended(payload)
	if err != nil {
		return
	}

	if extID == peer.ExtHandshakeID {
		// Extension handshake response — learn peer's ut_pex ID
		peerHS, err := peer.DecodeExtHandshake(data)
		if err != nil {
			return
		}
		if id, ok := peerHS.M["ut_pex"]; ok && id > 0 {
			c.pexRemoteID = uint8(id)
		}
		return
	}

	// PEX message (our local ID is 2)
	const localPexID uint8 = 2
	if extID == localPexID {
		pexMsg, err := peer.DecodePEX(data)
		if err != nil || pexMsg == nil {
			return
		}
		if len(pexMsg.Added) > 0 && c.PeerSink != nil {
			peers := make([]tracker.Peer, 0, len(pexMsg.Added))
			for _, p := range pexMsg.Added {
				peers = append(peers, tracker.Peer{IP: p.IP, Port: p.Port})
			}
			select {
			case c.PeerSink <- peers:
			default:
			}
		}
	}
}

// Close closes the connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// HasPiece returns whether the peer has the given piece.
// A nil bitfield means "have all" (BEP 6 have-all).
func (c *Client) HasPiece(index int) bool {
	if c.bitfield == nil {
		return true // have-all
	}
	return c.bitfield.HasPiece(index)
}

func (c *Client) sendInterested() error {
	if c.sentInterested {
		return nil
	}
	msg := &peer.Message{ID: peer.MsgInterested}
	if err := msg.Write(c.w); err != nil {
		return err
	}
	if err := c.w.Flush(); err != nil {
		return err
	}
	c.sentInterested = true
	return nil
}

func (c *Client) waitForUnchoke() error {
	for c.choked {
		msg, err := peer.ReadMessage(c.r)
		if err != nil {
			return err
		}
		if msg == nil {
			continue
		}
		switch msg.ID {
		case peer.MsgUnchoke:
			c.choked = false
		case peer.MsgChoke:
			c.choked = true
		case peer.MsgHave:
			idx, err := peer.ParseHave(msg.Payload)
			if err == nil {
				c.bitfield.SetPiece(int(idx))
			}
		case peer.MsgExtended:
			c.handleExtended(msg.Payload)
		case peer.MsgRequest:
			c.handleRequest(msg.Payload)
		}
	}
	return nil
}

// handleRequest serves an incoming piece request from a peer (upload while downloading).
func (c *Client) handleRequest(payload []byte) {
	if c.OnRequest == nil {
		return
	}
	index, begin, length, err := peer.ParseRequest(payload)
	if err != nil || length > 32*1024 {
		return
	}
	block := c.OnRequest(index, begin, length)
	if block == nil {
		return
	}
	msg := peer.NewPieceMessage(index, begin, block)
	_ = msg.Write(c.w)
	_ = c.w.Flush()
	c.uploaded.Add(int64(len(block)))
}

// DownloadPiece downloads a single piece from the peer.
// Returns the piece data and a release function to return the buffer to the pool.
// The release function may be nil if no pool is used. Caller must call release
// after consuming the data.
func (c *Client) DownloadPiece(pw PieceWork) ([]byte, func(), error) {
	_ = c.conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()

	if err := c.sendInterested(); err != nil {
		return nil, nil, err
	}
	if err := c.waitForUnchoke(); err != nil {
		return nil, nil, err
	}

	var buf []byte
	var release func()
	if c.piecePool != nil {
		bp := c.piecePool.Get().(*[]byte)
		buf = (*bp)[:pw.Length]
		release = func() { c.piecePool.Put(bp) }
	} else {
		buf = make([]byte, pw.Length)
	}

	fail := func(err error) ([]byte, func(), error) {
		if release != nil {
			release()
		}
		return nil, nil, err
	}

	downloaded := 0
	requested := 0
	backlog := 0

	for downloaded < pw.Length {
		flushed := false
		for backlog < c.maxPipeline && requested < pw.Length {
			blockSize := BlockSize
			if requested+blockSize > pw.Length {
				blockSize = pw.Length - requested
			}
			req := peer.NewRequestMessage(uint32(pw.Index), uint32(requested), uint32(blockSize))
			if err := req.Write(c.w); err != nil {
				return fail(err)
			}
			requested += blockSize
			backlog++
			flushed = false
		}
		if !flushed {
			if err := c.w.Flush(); err != nil {
				return fail(err)
			}
		}

		msg, err := peer.ReadMessage(c.r)
		if err != nil {
			return fail(err)
		}
		if msg == nil {
			continue
		}

		switch msg.ID {
		case peer.MsgPiece:
			idx, begin, block, err := peer.ParsePiece(msg.Payload)
			if err != nil {
				return fail(err)
			}
			if int(idx) != pw.Index {
				continue
			}
			if int(begin)+len(block) > len(buf) {
				return fail(fmt.Errorf("download: piece %d block out of bounds (begin=%d, len=%d, piece=%d)", pw.Index, begin, len(block), len(buf)))
			}
			copy(buf[begin:], block)
			downloaded += len(block)
			c.downloaded.Add(int64(len(block)))
			backlog--
		case peer.MsgChoke:
			c.choked = true
			return fail(fmt.Errorf("download: peer choked during piece %d", pw.Index))
		case peer.MsgHave:
			idx, err := peer.ParseHave(msg.Payload)
			if err == nil {
				c.bitfield.SetPiece(int(idx))
			}
		case peer.MsgReject:
			// BEP 6: peer rejected our request
			return fail(fmt.Errorf("download: peer rejected piece %d", pw.Index))
		case peer.MsgExtended:
			c.handleExtended(msg.Payload)
		}
	}

	hash := sha1.Sum(buf)
	if hash != pw.Hash {
		return fail(fmt.Errorf("download: piece %d hash mismatch", pw.Index))
	}

	return buf, release, nil
}

// startWorkers launches peer workers and returns a result channel.
// Each worker connects to a peer, grabs pieces from workCh, and sends results.
func startWorkers(peers []tracker.Peer, infoHash, peerID [20]byte, workCh chan PieceWork, progress *Progress) <-chan PieceResult {
	resultCh := make(chan PieceResult, cap(workCh))

	var wg sync.WaitGroup
	sem := make(chan struct{}, DefaultMaxPeers)

	for _, p := range peers {
		wg.Add(1)
		go func(p tracker.Peer) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			client, err := NewClient(p, infoHash, peerID)
			if err != nil {
				return
			}
			defer func() { _ = client.Close() }()

			progress.PeerConnect()
			defer progress.PeerDisconnect()

			for pw := range workCh {
				if !client.HasPiece(pw.Index) {
					workCh <- pw
					continue
				}

				data, release, err := client.DownloadPiece(pw)
				if err != nil {
					workCh <- pw
					return
				}

				resultCh <- PieceResult{Index: pw.Index, Data: data, Release: release}
			}
		}(p)
	}

	go func() {
		wg.Wait()
		close(workCh)
	}()

	return resultCh
}

func buildWorkQueue(tf *torrent.TorrentFile, totalLength int64) chan PieceWork {
	numPieces := len(tf.Info.PieceHashes)
	workCh := make(chan PieceWork, numPieces)
	for i, hash := range tf.Info.PieceHashes {
		length := int(tf.Info.PieceLength)
		remaining := int(totalLength) - i*int(tf.Info.PieceLength)
		if remaining < length {
			length = remaining
		}
		workCh <- PieceWork{Index: i, Hash: hash, Length: length}
	}
	return workCh
}

// Download downloads all pieces of a torrent concurrently and returns the assembled data.
func Download(tf *torrent.TorrentFile, peerID [20]byte, peers []tracker.Peer) ([]byte, error) {
	tl := storage.TotalSize(tf)
	numPieces := len(tf.Info.PieceHashes)
	peers = deduplicatePeers(peers)

	workCh := buildWorkQueue(tf, tl)
	progress := NewProgress(numPieces, tl)
	resultCh := startWorkers(peers, tf.InfoHash, peerID, workCh, progress)

	results := make([][]byte, numPieces)
	for completed := 0; completed < numPieces; {
		select {
		case res := <-resultCh:
			results[res.Index] = res.Data
			progress.Add(len(res.Data))
			completed++
			fmt.Print(progress)
		case <-time.After(2 * time.Minute):
			return nil, fmt.Errorf("download: timed out at %d/%d pieces", completed, numPieces)
		}
	}
	fmt.Println()

	buf := make([]byte, 0, tl)
	for _, data := range results {
		buf = append(buf, data...)
	}
	return buf, nil
}

// DownloadToFile downloads all pieces concurrently and writes directly to a file.
func DownloadToFile(tf *torrent.TorrentFile, peerID [20]byte, peers []tracker.Peer, path string) error {
	tl := storage.TotalSize(tf)
	numPieces := len(tf.Info.PieceHashes)
	peers = deduplicatePeers(peers)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := f.Truncate(tl); err != nil {
		return fmt.Errorf("download: truncate failed: %w", err)
	}

	workCh := buildWorkQueue(tf, tl)
	progress := NewProgress(numPieces, tl)
	resultCh := startWorkers(peers, tf.InfoHash, peerID, workCh, progress)

	pieceLength := int(tf.Info.PieceLength)
	for completed := 0; completed < numPieces; {
		select {
		case res := <-resultCh:
			offset := int64(res.Index) * int64(pieceLength)
			if _, err := f.WriteAt(res.Data, offset); err != nil {
				return fmt.Errorf("download: write piece %d failed: %w", res.Index, err)
			}
			progress.Add(len(res.Data))
			completed++
			fmt.Print(progress)
		case <-time.After(2 * time.Minute):
			return fmt.Errorf("download: timed out at %d/%d pieces", completed, numPieces)
		}
	}
	fmt.Println()

	return nil
}

// DownloadParams consolidates parameters for a download session.
type DownloadParams struct {
	TF          *torrent.TorrentFile
	PeerID      [20]byte
	Peers       []tracker.Peer        // initial peers
	PeerCh      <-chan []tracker.Peer // dynamic peer injection (nil = disabled)
	PeerSink    chan<- []tracker.Peer // for PEX: discovered peers are sent here (nil = disabled)
	Path        string
	Progress    *Progress
	Cfg         DownloadConfig
	Have        peer.Bitfield                            // pieces already downloaded (for resume)
	OnPiece     func(index int)                          // called when a piece is downloaded (for upload during download)
	OnRequest   func(index, begin, length uint32) []byte // serve incoming piece requests
	WebSeedURLs []string                                 // BEP 19: HTTP URLs for downloading pieces
}

// DownloadToFileCtx is like DownloadToFile but accepts a context for cancellation.
func DownloadToFileCtx(ctx context.Context, tf *torrent.TorrentFile, peerID [20]byte, peers []tracker.Peer, path string, progress *Progress) error {
	return DownloadWithParams(ctx, DownloadParams{
		TF: tf, PeerID: peerID, Peers: peers, Path: path, Progress: progress, Cfg: DefaultDownloadConfig(),
	})
}

// DownloadToFileCtxWithConfig is like DownloadToFileCtx but with tunable parameters.
func DownloadToFileCtxWithConfig(ctx context.Context, tf *torrent.TorrentFile, peerID [20]byte, peers []tracker.Peer, path string, progress *Progress, cfg DownloadConfig) error {
	return DownloadWithParams(ctx, DownloadParams{
		TF: tf, PeerID: peerID, Peers: peers, Path: path, Progress: progress, Cfg: cfg,
	})
}

// writeAtCloser abstracts single-file and multi-file write targets.
type writeAtCloser interface {
	WriteAt([]byte, int64) (int, error)
	Close() error
}

// DownloadWithParams runs a download session with full control over parameters.
// Supports dynamic peer injection via PeerCh and resume via Have bitfield.
func DownloadWithParams(ctx context.Context, p DownloadParams) error {
	tl := storage.TotalSize(p.TF)
	numPieces := len(p.TF.Info.PieceHashes)
	peers := deduplicatePeers(p.Peers)

	// Open write target: single file or multi-file directory
	var w writeAtCloser
	if storage.IsMultiFile(p.TF) {
		mw, err := storage.NewMultiFileWriter(p.Path, p.TF)
		if err != nil {
			return err
		}
		if err := mw.PreallocateFiles(); err != nil {
			return fmt.Errorf("download: preallocate failed: %w", err)
		}
		w = mw
	} else {
		f, err := os.OpenFile(p.Path, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return err
		}
		if err := f.Truncate(tl); err != nil {
			_ = f.Close()
			return fmt.Errorf("download: truncate failed: %w", err)
		}
		w = f
	}
	defer func() { _ = w.Close() }()

	// Count already-completed pieces and build work queue for remaining
	alreadyDone := 0
	if p.Have != nil {
		for i := range numPieces {
			if p.Have.HasPiece(i) {
				alreadyDone++
			}
		}
	}

	// Build piece list for remaining work
	var pieces []PieceWork
	for i, hash := range p.TF.Info.PieceHashes {
		if p.Have != nil && p.Have.HasPiece(i) {
			continue
		}
		length := int(p.TF.Info.PieceLength)
		rem := int(tl) - i*int(p.TF.Info.PieceLength)
		if rem < length {
			length = rem
		}
		pieces = append(pieces, PieceWork{Index: i, Hash: hash, Length: length})
	}
	remaining := len(pieces)

	// Initialize progress with already-completed pieces
	if alreadyDone > 0 {
		p.Progress.SetInitial(alreadyDone, tl, int64(p.TF.Info.PieceLength))
	}

	pq := NewPieceQueue(pieces)
	resultCh := runWorkers(ctx, peers, p.TF.InfoHash, p.PeerID, pq, p.PeerCh, p.PeerSink, p.Progress, p.Cfg, p.OnRequest, p.TF, p.WebSeedURLs)

	pieceLength := int(p.TF.Info.PieceLength)
	for completed := 0; completed < remaining; {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case res, ok := <-resultCh:
			if !ok {
				if completed < remaining {
					return fmt.Errorf("download: workers finished at %d/%d pieces", completed+alreadyDone, numPieces)
				}
				return nil
			}
			offset := int64(res.Index) * int64(pieceLength)
			if _, err := w.WriteAt(res.Data, offset); err != nil {
				res.Free()
				return fmt.Errorf("download: write piece %d failed: %w", res.Index, err)
			}
			p.Progress.Add(len(res.Data))
			res.Free()
			if p.OnPiece != nil {
				p.OnPiece(res.Index)
			}
			completed++
		}
	}

	return nil
}

// runWorkers launches peer workers with rarest-first piece selection and dynamic peer injection.
func runWorkers(ctx context.Context, initialPeers []tracker.Peer, infoHash, peerID [20]byte, pq *PieceQueue, peerCh <-chan []tracker.Peer, peerSink chan<- []tracker.Peer, progress *Progress, cfg DownloadConfig, onRequest func(uint32, uint32, uint32) []byte, tf *torrent.TorrentFile, webSeedURLs []string) <-chan PieceResult {
	resultCh := make(chan PieceResult, 64)

	pm := NewPeerManager(DefaultUnchokeSlots)
	go pm.Run(ctx)

	// Create piece buffer pool based on max piece size
	piecePool := newPiecePool(pq.maxPieceLen())

	sem := make(chan struct{}, cfg.MaxPeers)
	seen := &sync.Map{}
	var preferEncrypt atomic.Bool

	var activeWorkers sync.WaitGroup

	spawnWorker := func(p tracker.Peer) {
		addr := p.String()
		if _, loaded := seen.LoadOrStore(addr, true); loaded {
			return
		}

		activeWorkers.Add(1)
		go func() {
			defer activeWorkers.Done()
			// Allow reconnection after disconnect
			defer seen.Delete(addr)

			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
				defer func() { <-sem }()
			}

			var client *Client
			var err error
			if cfg.Encryption {
				client, err = NewClientEncrypted(p, infoHash, peerID, cfg.DialTimeout, &preferEncrypt, cfg.EnableUTP, cfg.DisablePEX)
			} else {
				client, err = newClientFull(p, infoHash, peerID, cfg.DialTimeout, false, cfg.EnableUTP, cfg.DisablePEX)
			}
			if err != nil {
				return
			}
			client.maxPipeline = cfg.MaxPipeline
			client.piecePool = piecePool
			if peerSink != nil {
				client.PeerSink = peerSink
			}
			if onRequest != nil {
				client.OnRequest = onRequest
			}
			defer func() { _ = client.Close() }()

			pq.AddPeerBitfield(client.bitfield)
			defer pq.RemovePeerBitfield(client.bitfield)

			pm.Register(client)
			defer pm.Unregister(client)

			progress.PeerConnect()
			defer progress.PeerDisconnect()

			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				pw, ok := pq.Pick(client.HasPiece)
				if !ok {
					select {
					case <-ctx.Done():
						return
					case <-pq.Wait():
						continue
					}
				}

				// In endgame, check if another worker already completed this piece
				if pq.IsDone(pw.Index) {
					continue
				}

				data, release, err := client.DownloadPiece(pw)
				if err != nil {
					pq.Return(pw)
					return
				}

				// In endgame, another worker may have finished first
				if pq.IsDone(pw.Index) {
					if release != nil {
						release()
					}
					continue
				}

				pq.Complete(pw.Index)

				select {
				case resultCh <- PieceResult{Index: pw.Index, Data: data, Release: release}:
				case <-ctx.Done():
					if release != nil {
						release()
					}
					return
				}
			}
		}()
	}

	// retryPeers goroutine: re-attempt disconnected peers with exponential backoff
	retryDone := make(chan struct{})
	go func() {
		defer close(retryDone)
		interval := 5 * time.Second
		const maxInterval = 30 * time.Second
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if pq.Remaining() == 0 {
					return
				}
				// Re-try initial peers that have disconnected (seen was cleared on exit)
				for _, p := range initialPeers {
					spawnWorker(p)
				}
				// Exponential backoff: 5s → 10s → 20s → 30s (capped)
				interval = interval * 2
				if interval > maxInterval {
					interval = maxInterval
				}
				timer.Reset(interval)
			}
		}
	}()

	for _, p := range initialPeers {
		spawnWorker(p)
	}

	var feederDone chan struct{}
	if peerCh != nil {
		feederDone = make(chan struct{})
		go func() {
			defer close(feederDone)
			for {
				select {
				case <-ctx.Done():
					return
				case batch, ok := <-peerCh:
					if !ok {
						return
					}
					for _, p := range batch {
						spawnWorker(p)
					}
				}
			}
		}()
	}

	// BEP 19: webseed workers (only http/https URLs)
	for _, wsURL := range webSeedURLs {
		if !validWebSeedURL(wsURL) {
			continue
		}
		activeWorkers.Add(1)
		go func(baseURL string) {
			defer activeWorkers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				pw, ok := pq.Pick(func(int) bool { return true }) // HTTP has all pieces
				if !ok {
					select {
					case <-ctx.Done():
						return
					case <-pq.Wait():
						continue
					}
				}
				if pq.IsDone(pw.Index) {
					continue
				}
				data, err := downloadPieceHTTP(ctx, baseURL, tf, pw)
				if err != nil {
					pq.Return(pw)
					return // HTTP source failed, stop this worker
				}
				if pq.IsDone(pw.Index) {
					continue
				}
				pq.Complete(pw.Index)
				select {
				case resultCh <- PieceResult{Index: pw.Index, Data: data}:
				case <-ctx.Done():
					return
				}
			}
		}(wsURL)
	}

	go func() {
		if feederDone != nil {
			<-feederDone
		}
		<-retryDone // wait for retry goroutine to stop spawning workers
		activeWorkers.Wait()
		close(resultCh)
	}()

	return resultCh
}

func deduplicatePeers(peers []tracker.Peer) []tracker.Peer {
	seen := make(map[string]bool, len(peers))
	result := make([]tracker.Peer, 0, len(peers))
	for _, p := range peers {
		key := p.String()
		if !seen[key] {
			seen[key] = true
			result = append(result, p)
		}
	}
	return result
}
