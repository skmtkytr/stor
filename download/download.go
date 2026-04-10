package download

import (
	"bufio"
	"context"
	"crypto/sha1"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/tracker"
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
	MaxPeers    int // max concurrent peer connections
	MaxPipeline int // outstanding requests per peer
	DialTimeout int // peer dial timeout in seconds
}

// DefaultDownloadConfig returns default download config.
func DefaultDownloadConfig() DownloadConfig {
	return DownloadConfig{
		MaxPeers:    DefaultMaxPeers,
		MaxPipeline: DefaultMaxPipeline,
		DialTimeout: DefaultDialTimeout,
	}
}

// PieceResult contains a downloaded and verified piece.
type PieceResult struct {
	Index int
	Data  []byte
}

// PieceWork describes a piece to download.
type PieceWork struct {
	Index  int
	Hash   [20]byte
	Length int
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

	// Speed tracking
	downloaded int64     // bytes downloaded from this peer
	uploaded   int64     // bytes uploaded to this peer
	speedStart time.Time // start of current measurement window
	lastSpeed  float64   // bytes/sec from last measurement

	// PEX (BEP 11)
	pexRemoteID uint8                 // remote's ut_pex message ID (0 = not supported)
	PeerSink    chan<- []tracker.Peer // discovered peers from PEX are sent here
}

// Speed returns the current download speed in bytes/sec.
func (c *Client) Speed() float64 {
	elapsed := time.Since(c.speedStart).Seconds()
	if elapsed < 1 {
		return c.lastSpeed
	}
	c.lastSpeed = float64(c.downloaded) / elapsed
	return c.lastSpeed
}

// ResetSpeed resets the speed measurement window.
func (c *Client) ResetSpeed() {
	c.lastSpeed = c.Speed()
	c.downloaded = 0
	c.speedStart = time.Now()
}

// UploadSpeed returns bytes/sec uploaded to this peer.
func (c *Client) UploadSpeed() float64 {
	elapsed := time.Since(c.speedStart).Seconds()
	if elapsed < 1 {
		return 0
	}
	return float64(c.uploaded) / elapsed
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
	conn, err := net.DialTimeout("tcp", p.String(), time.Duration(dialTimeoutSec)*time.Second)
	if err != nil {
		return nil, fmt.Errorf("download: connect to %s failed: %w", p, err)
	}

	closeOnErr := func() { _ = conn.Close() }

	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	hs := &peer.Handshake{InfoHash: infoHash, PeerID: peerID, Extensions: true}
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
	}

	msg, err := peer.ReadMessage(c.r)
	_ = conn.SetDeadline(time.Time{})
	if err != nil {
		closeOnErr()
		return nil, fmt.Errorf("download: read bitfield failed: %w", err)
	}
	if msg != nil && msg.ID == peer.MsgBitfield {
		c.bitfield = peer.Bitfield(msg.Payload)
	}

	// BEP 10: send extension handshake if peer supports extensions
	if resp.Extensions {
		const localPexID uint8 = 2
		extHS := &peer.ExtHandshake{
			M: map[string]int64{"ut_pex": int64(localPexID)},
			V: "stor/0.1",
		}
		payload, _ := peer.EncodeExtHandshake(extHS)
		extMsg := peer.NewExtendedMessage(peer.ExtHandshakeID, payload)
		if err := extMsg.Write(c.w); err == nil {
			_ = c.w.Flush()
		}
		// We'll read the peer's ext handshake response during message loop
		// and set pexRemoteID then
	}

	return c, nil
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
func (c *Client) HasPiece(index int) bool {
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
		}
	}
	return nil
}

// DownloadPiece downloads a single piece from the peer.
func (c *Client) DownloadPiece(pw PieceWork) ([]byte, error) {
	_ = c.conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()

	if err := c.sendInterested(); err != nil {
		return nil, err
	}
	if err := c.waitForUnchoke(); err != nil {
		return nil, err
	}

	buf := make([]byte, pw.Length)
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
				return nil, err
			}
			requested += blockSize
			backlog++
			flushed = false
		}
		if !flushed {
			if err := c.w.Flush(); err != nil {
				return nil, err
			}
		}

		msg, err := peer.ReadMessage(c.r)
		if err != nil {
			return nil, err
		}
		if msg == nil {
			continue
		}

		switch msg.ID {
		case peer.MsgPiece:
			idx, begin, block, err := peer.ParsePiece(msg.Payload)
			if err != nil {
				return nil, err
			}
			if int(idx) != pw.Index {
				continue
			}
			copy(buf[begin:], block)
			downloaded += len(block)
			c.downloaded += int64(len(block))
			backlog--
		case peer.MsgChoke:
			c.choked = true
			return nil, fmt.Errorf("download: peer choked during piece %d", pw.Index)
		case peer.MsgHave:
			idx, err := peer.ParseHave(msg.Payload)
			if err == nil {
				c.bitfield.SetPiece(int(idx))
			}
		case peer.MsgExtended:
			c.handleExtended(msg.Payload)
		}
	}

	hash := sha1.Sum(buf)
	if hash != pw.Hash {
		return nil, fmt.Errorf("download: piece %d hash mismatch", pw.Index)
	}

	return buf, nil
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

				data, err := client.DownloadPiece(pw)
				if err != nil {
					workCh <- pw
					return
				}

				resultCh <- PieceResult{Index: pw.Index, Data: data}
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

// TotalSize returns the total size of the torrent in bytes.
func TotalSize(tf *torrent.TorrentFile) int64 {
	if tf.Info.Length > 0 {
		return tf.Info.Length
	}
	var total int64
	for _, f := range tf.Info.Files {
		total += f.Length
	}
	return total
}

// Download downloads all pieces of a torrent concurrently and returns the assembled data.
func Download(tf *torrent.TorrentFile, peerID [20]byte, peers []tracker.Peer) ([]byte, error) {
	tl := TotalSize(tf)
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
	tl := TotalSize(tf)
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
	TF       *torrent.TorrentFile
	PeerID   [20]byte
	Peers    []tracker.Peer        // initial peers
	PeerCh   <-chan []tracker.Peer // dynamic peer injection (nil = disabled)
	PeerSink chan<- []tracker.Peer // for PEX: discovered peers are sent here (nil = disabled)
	Path     string
	Progress *Progress
	Cfg      DownloadConfig
	Have     peer.Bitfield // pieces already downloaded (for resume)
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

// DownloadWithParams runs a download session with full control over parameters.
// Supports dynamic peer injection via PeerCh and resume via Have bitfield.
func DownloadWithParams(ctx context.Context, p DownloadParams) error {
	tl := TotalSize(p.TF)
	numPieces := len(p.TF.Info.PieceHashes)
	peers := deduplicatePeers(p.Peers)

	// Open file for resume (preserve existing data)
	f, err := os.OpenFile(p.Path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := f.Truncate(tl); err != nil {
		return fmt.Errorf("download: truncate failed: %w", err)
	}

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
	resultCh := runWorkers(ctx, peers, p.TF.InfoHash, p.PeerID, pq, p.PeerCh, p.PeerSink, p.Progress, p.Cfg)

	pieceLength := int(p.TF.Info.PieceLength)
	for completed := 0; completed < remaining; {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case res, ok := <-resultCh:
			if !ok {
				if completed < numPieces {
					return fmt.Errorf("download: workers finished at %d/%d pieces", completed, numPieces)
				}
				return nil
			}
			offset := int64(res.Index) * int64(pieceLength)
			if _, err := f.WriteAt(res.Data, offset); err != nil {
				return fmt.Errorf("download: write piece %d failed: %w", res.Index, err)
			}
			p.Progress.Add(len(res.Data))
			completed++
		}
	}

	return nil
}

// runWorkers launches peer workers with rarest-first piece selection and dynamic peer injection.
func runWorkers(ctx context.Context, initialPeers []tracker.Peer, infoHash, peerID [20]byte, pq *PieceQueue, peerCh <-chan []tracker.Peer, peerSink chan<- []tracker.Peer, progress *Progress, cfg DownloadConfig) <-chan PieceResult {
	resultCh := make(chan PieceResult, 64)

	pm := NewPeerManager(DefaultUnchokeSlots)
	go pm.Run(ctx)

	sem := make(chan struct{}, cfg.MaxPeers)
	seen := &sync.Map{}

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

			client, err := NewClientWithTimeout(p, infoHash, peerID, cfg.DialTimeout)
			if err != nil {
				return
			}
			client.maxPipeline = cfg.MaxPipeline
			if peerSink != nil {
				client.PeerSink = peerSink
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

				data, err := client.DownloadPiece(pw)
				if err != nil {
					pq.Return(pw)
					return
				}

				// In endgame, another worker may have finished first
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
		}()
	}

	// retryPeers goroutine: periodically re-attempt disconnected peers
	retryDone := make(chan struct{})
	go func() {
		defer close(retryDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if pq.Remaining() == 0 {
					return
				}
				// Re-try initial peers that have disconnected (seen was cleared on exit)
				for _, p := range initialPeers {
					spawnWorker(p)
				}
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

	go func() {
		if feederDone != nil {
			<-feederDone
		}
		<-retryDone
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
