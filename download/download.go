package download

import (
	"bufio"
	"context"
	"crypto/sha1"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
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
	DefaultMaxPeers    = 500
	DefaultDialTimeout = 10 // seconds

	// maxUnexpectedMessages is the limit on unexpected/wrong-index messages
	// per piece before aborting. Prevents DoS from malicious peers.
	maxUnexpectedMessages = 100
)

// DownloadConfig holds tunable parameters for the download engine.
type DownloadConfig struct {
	MaxPeers      int           // max concurrent peer connections
	MaxPipeline   int           // outstanding requests per peer
	DialTimeout   int           // peer dial timeout in seconds
	Encryption    bool          // attempt MSE/PE encryption (default: true)
	EnableUTP     bool          // try uTP before TCP for peer connections
	DisablePEX    bool          // BEP 27: do not advertise or use PEX
	DialSem       chan struct{} // shared global dial semaphore (nil = create per-torrent)
	RetryInterval time.Duration // interval between peer retry sweeps (0 = default 60s)

	// PrioritizeFirstLast: when true, the first and last pieces of every
	// torrent are picked before the regular rarest-first selection. Useful
	// for streaming media files whose container metadata lives at the
	// edges of the file. Off by default (deluge parity:
	// `prioritize_first_last_pieces`).
	PrioritizeFirstLast bool
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
	conn     net.Conn
	r        *bufio.Reader
	w        *bufio.Writer
	wmu      sync.Mutex // protects w from concurrent writes (BroadcastHave vs DownloadPiece)
	stateMu  sync.Mutex // protects choked, choking, sentInterested
	peerID   [20]byte
	infoHash [20]byte
	// Remote peer's piece set. The bitfield is updated as Have/Bitfield/
	// HaveNone messages arrive; the haveAll flag is set on MsgHaveAll and
	// short-circuits HasPiece without requiring an N-bit-wide bitfield.
	// We deliberately do NOT use `nil bitfield` as a have-all sentinel:
	// a freshly-constructed Client has no information yet and would be
	// misclassified as a seeder, which leaks into UI Snapshot and into
	// piece-selection decisions. Default = empty (have-none).
	bitfield       peer.Bitfield
	haveAll        bool
	choked         bool // we are choked by peer
	choking        bool // we are choking peer
	sentInterested bool
	maxPipeline    int
	Addr           string // peer address for identification

	// Peer metadata (for UI / introspection)
	Incoming     bool     // true if this connection was accepted (remote dialed us)
	UsingUTP     bool     // true if the underlying transport is uTP (not TCP)
	Encrypted    bool     // true if MSE/PE (RC4) was negotiated
	RemotePeerID [20]byte // peer ID from remote handshake

	// Speed tracking.
	// downloaded/uploaded + speedStart drive the rechoke ranking (BEP 3):
	// cumulative bytes over the last rechoke window, reset by ResetSpeed.
	// downCounter/upCounter accumulate bytes between rate-registry ticks
	// (the shared 1 Hz sampler snaps and resets them). downRate/upRate
	// are the short-window EMA estimators written by the registry and
	// read by Snapshot — same 1-second cadence and 0.8/0.2 smoothing as
	// the torrent-wide Progress EMA so per-peer UI rates stay in sync
	// with the top-row total.
	downloaded  atomic.Int64
	uploaded    atomic.Int64
	speedMu     sync.Mutex
	speedStart  time.Time
	lastSpeed   atomic.Int64 // bytes/sec
	downCounter atomic.Int64 // bytes since last tick (fed by DownloadPiece)
	upCounter   atomic.Int64 // bytes since last tick (fed by handleRequest/upload)
	downRate    atomic.Int64 // EMA bytes/sec, written by rateRegistry
	upRate      atomic.Int64 // EMA bytes/sec, written by rateRegistry
	unregRate   func()       // deregister from rateRegistry on Close; nil for zero-value test clients

	// PEX (BEP 11)
	pexRemoteID uint8                 // remote's ut_pex message ID (0 = not supported)
	PeerSink    chan<- []tracker.Peer // discovered peers from PEX are sent here

	// Upload while downloading: callback to serve incoming requests
	OnRequest func(index, begin, length uint32) []byte

	numPieces int // total piece count; used to validate incoming requests (0 = skip validation)

	// Piece buffer pool (set by runWorkers for buffer reuse)
	piecePool  *sync.Pool
	disablePEX bool      // BEP 27: suppress PEX for private torrents
	fastExt    bool      // BEP 6: peer supports fast extension
	progress   *Progress // for block-level speed tracking (nil in legacy mode)

	// disconnectReason is the last reason recorded for this client's
	// disconnection. Read by PeerManager.Unregister to populate the
	// PeerDisconnected event. Protected by reasonMu so concurrent setters
	// (worker goroutine vs handshake-time failures) don't race.
	reasonMu         sync.Mutex
	disconnectReason string
}

// SetDisconnectReason records why this client disconnected. The first
// non-empty reason wins; later calls are no-ops so we don't overwrite a
// specific failure (e.g. "protocol_error: ...") with a generic "normal"
// from a deferred Close.
func (c *Client) SetDisconnectReason(reason string) {
	if reason == "" {
		return
	}
	c.reasonMu.Lock()
	if c.disconnectReason == "" {
		c.disconnectReason = reason
	}
	c.reasonMu.Unlock()
}

// DisconnectReason returns the recorded reason, or "" if none was set.
func (c *Client) DisconnectReason() string {
	c.reasonMu.Lock()
	r := c.disconnectReason
	c.reasonMu.Unlock()
	return r
}

// Speed returns the current download speed in bytes/sec.
func (c *Client) Speed() float64 {
	c.speedMu.Lock()
	start := c.speedStart
	c.speedMu.Unlock()
	elapsed := time.Since(start).Seconds()
	if elapsed < 1 {
		return float64(c.lastSpeed.Load())
	}
	speed := int64(float64(c.downloaded.Load()) / elapsed)
	c.lastSpeed.Store(speed)
	return float64(speed)
}

// ResetSpeed resets the speed measurement window. Both directions are
// zeroed — otherwise UploadSpeed would keep reporting the lifetime average
// (cumulative bytes / time-since-start) long after the peer stopped
// uploading, which is what users see as "UP rate showing for a peer that
// isn't uploading".
func (c *Client) ResetSpeed() {
	c.Speed() // update lastSpeed
	c.downloaded.Store(0)
	c.uploaded.Store(0)
	c.speedMu.Lock()
	c.speedStart = time.Now()
	c.speedMu.Unlock()
}

// UploadSpeed returns bytes/sec uploaded to this peer.
func (c *Client) UploadSpeed() float64 {
	c.speedMu.Lock()
	start := c.speedStart
	c.speedMu.Unlock()
	elapsed := time.Since(start).Seconds()
	if elapsed < 1 {
		return 0
	}
	return float64(c.uploaded.Load()) / elapsed
}

// SendChoke sends a choke message to the peer.
func (c *Client) SendChoke() error {
	c.stateMu.Lock()
	if c.choking {
		c.stateMu.Unlock()
		return nil
	}
	c.stateMu.Unlock()
	c.wmu.Lock()
	defer c.wmu.Unlock()
	msg := &peer.Message{ID: peer.MsgChoke}
	if err := msg.Write(c.w); err != nil {
		return err
	}
	if err := c.w.Flush(); err != nil {
		return err
	}
	c.stateMu.Lock()
	c.choking = true
	c.stateMu.Unlock()
	return nil
}

// SendUnchoke sends an unchoke message to the peer.
func (c *Client) SendUnchoke() error {
	c.stateMu.Lock()
	if !c.choking {
		c.stateMu.Unlock()
		return nil
	}
	c.stateMu.Unlock()
	c.wmu.Lock()
	defer c.wmu.Unlock()
	msg := &peer.Message{ID: peer.MsgUnchoke}
	if err := msg.Write(c.w); err != nil {
		return err
	}
	if err := c.w.Flush(); err != nil {
		return err
	}
	c.stateMu.Lock()
	c.choking = false
	c.stateMu.Unlock()
	return nil
}

// IsChoking returns whether we are choking this peer.
func (c *Client) IsChoking() bool {
	c.stateMu.Lock()
	v := c.choking
	c.stateMu.Unlock()
	return v
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

	_, isUTP := rawConn.(*utp.Conn)
	c := &Client{
		conn:         conn,
		r:            bufio.NewReaderSize(conn, 64*1024),
		w:            bufio.NewWriterSize(conn, 32*1024),
		peerID:       peerID,
		infoHash:     infoHash,
		choked:       true, // we are choked by peer (default per BT protocol)
		choking:      true, // we are choking peer (default per BT protocol)
		maxPipeline:  DefaultMaxPipeline,
		Addr:         p.String(),
		speedStart:   time.Now(),
		disablePEX:   noPEX,
		fastExt:      resp.FastExtension,
		Incoming:     false, // outgoing: we dialed
		UsingUTP:     isUTP,
		Encrypted:    encrypt,
		RemotePeerID: resp.PeerID,
	}
	attachRateTargets(c)

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
			c.haveAll = false
		case peer.MsgHaveAll:
			// BEP 6: peer has all pieces. Track via the explicit flag
			// instead of nil-bitfield sentinel so HasPiece / Snapshot
			// can distinguish "seeder" from "no info yet".
			c.haveAll = true
		case peer.MsgHaveNone:
			// BEP 6: peer has no pieces
			c.bitfield = peer.Bitfield{}
			c.haveAll = false
		case peer.MsgExtended:
			c.handleExtended(msg.Payload)
		case peer.MsgUnchoke:
			c.choked = false
		case peer.MsgInterested:
			// will be handled after init
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

// Close closes the connection and deregisters the client from the shared
// rate sampler. Safe to call multiple times.
func (c *Client) Close() error {
	if c.unregRate != nil {
		c.unregRate()
		c.unregRate = nil
	}
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// ValidateBitfield checks that the peer's bitfield is the expected size for numPieces.
// Returns an error if the bitfield is oversized (potential DoS).
// haveAll-via-flag and empty (have-none) bitfields are always valid.
func (c *Client) ValidateBitfield(numPieces int) error {
	if c.haveAll || len(c.bitfield) == 0 {
		return nil
	}
	expected := (numPieces + 7) / 8
	if len(c.bitfield) != expected {
		return fmt.Errorf("download: bitfield size %d doesn't match expected %d for %d pieces", len(c.bitfield), expected, numPieces)
	}
	return nil
}

// HasPiece returns whether the peer has the given piece.
func (c *Client) HasPiece(index int) bool {
	if c.haveAll {
		return true
	}
	return c.bitfield.HasPiece(index)
}

// safeBitfieldSet sets a piece in the bitfield. No-op when haveAll is
// already true (the flag implies every bit is set, no allocation needed).
func (c *Client) safeBitfieldSet(index int) {
	if c.haveAll {
		return
	}
	if c.bitfield != nil {
		c.bitfield.SetPiece(index)
	}
}

// SendBitfield sends our bitfield to the peer so they know what pieces
// we have and can request them from us.
func (c *Client) SendBitfield(bf peer.Bitfield) error {
	if len(bf) == 0 {
		return nil
	}
	hasAny := false
	for _, b := range bf {
		if b != 0 {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return nil
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	msg := &peer.Message{ID: peer.MsgBitfield, Payload: bf}
	if err := msg.Write(c.w); err != nil {
		return err
	}
	return c.w.Flush()
}

// SendHave sends a have message for a single piece. Thread-safe.
func (c *Client) SendHave(index int) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	msg := peer.NewHaveMessage(uint32(index))
	if err := msg.Write(c.w); err != nil {
		return err
	}
	return c.w.Flush()
}

func (c *Client) sendInterested() error {
	if c.sentInterested {
		return nil
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
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
				c.safeBitfieldSet(int(idx))
			}
		case peer.MsgInterested:
			_ = c.SendUnchoke()
		case peer.MsgNotInterested:
			// ignored
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
	// Validate index against known piece count (if set)
	if c.numPieces > 0 && int(index) >= c.numPieces {
		return
	}
	block := c.OnRequest(index, begin, length)
	if block == nil {
		return
	}
	msg := peer.NewPieceMessage(index, begin, block)
	c.wmu.Lock()
	_ = msg.Write(c.w)
	_ = c.w.Flush()
	c.wmu.Unlock()
	n := int64(len(block))
	c.uploaded.Add(n)
	c.upCounter.Add(n)
	if c.progress != nil {
		c.progress.AddUploadBytes(n)
	}
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
	noProgressCount := 0

	for downloaded < pw.Length {
		flushed := false
		c.wmu.Lock()
		for backlog < c.maxPipeline && requested < pw.Length {
			blockSize := BlockSize
			if requested+blockSize > pw.Length {
				blockSize = pw.Length - requested
			}
			req := peer.NewRequestMessage(uint32(pw.Index), uint32(requested), uint32(blockSize))
			if err := req.Write(c.w); err != nil {
				c.wmu.Unlock()
				return fail(err)
			}
			requested += blockSize
			backlog++
			flushed = false
		}
		if !flushed {
			if err := c.w.Flush(); err != nil {
				c.wmu.Unlock()
				return fail(err)
			}
		}
		c.wmu.Unlock()

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
				backlog--
				noProgressCount++
				if noProgressCount > maxUnexpectedMessages {
					return fail(fmt.Errorf("download: piece %d too many unexpected messages from peer", pw.Index))
				}
				// Allow re-requesting lost blocks
				if backlog <= 0 {
					requested = downloaded
				}
				continue
			}
			noProgressCount = 0
			if int64(begin)+int64(len(block)) > int64(len(buf)) {
				return fail(fmt.Errorf("download: piece %d block out of bounds (begin=%d, len=%d, piece=%d)", pw.Index, begin, len(block), len(buf)))
			}
			copy(buf[begin:], block)
			n := int64(len(block))
			downloaded += int(n)
			c.downloaded.Add(n)
			c.downCounter.Add(n)
			if c.progress != nil {
				c.progress.AddBytes(n)
			}
			backlog--
		case peer.MsgChoke:
			c.choked = true
			return fail(fmt.Errorf("download: peer choked during piece %d", pw.Index))
		case peer.MsgUnchoke:
			c.choked = false
		case peer.MsgHave:
			idx, err := peer.ParseHave(msg.Payload)
			if err == nil {
				c.safeBitfieldSet(int(idx))
			}
		case peer.MsgInterested:
			// Peer wants our data — immediately unchoke them
			_ = c.SendUnchoke()
		case peer.MsgNotInterested:
			// ignored
		case peer.MsgRequest:
			c.handleRequest(msg.Payload)
		case peer.MsgReject:
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
		length := tf.Info.PieceLength
		remaining := totalLength - int64(i)*tf.Info.PieceLength
		if remaining < length {
			length = remaining
		}
		workCh <- PieceWork{Index: i, Hash: hash, Length: int(length)}
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

	pieceLength := tf.Info.PieceLength
	for completed := 0; completed < numPieces; {
		select {
		case res := <-resultCh:
			offset := int64(res.Index) * pieceLength
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
	TF           *torrent.TorrentFile
	PeerID       [20]byte
	Peers        []tracker.Peer        // initial peers
	PeerCh       <-chan []tracker.Peer // dynamic peer injection (nil = disabled)
	PeerSink     chan<- []tracker.Peer // for PEX: discovered peers are sent here (nil = disabled)
	Path         string
	Progress     *Progress
	Cfg          DownloadConfig
	Have         peer.Bitfield                            // pieces already downloaded (for resume)
	SkipMask     peer.Bitfield                            // pieces fully inside skipped files; not queued for download
	OnPiece      func(index int)                          // called when a piece is downloaded (for upload during download)
	OnRequest    func(index, begin, length uint32) []byte // serve incoming piece requests (set automatically if nil)
	HaveBF       peer.Bitfield                            // live bitfield updated by OnPiece (for upload-while-downloading)
	WebSeedURLs  []string                                 // BEP 19: HTTP URLs for downloading pieces
	OnPeerMgr    func(*PeerManager)                       // called once after PeerManager creation (for UI introspection)
	OnPieceQueue func(*PieceQueue)                        // called once after PieceQueue creation (for runtime skip-mask updates)
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

	// Auto-wire OnRequest: read from the write target (which is also readable)
	// so upload-while-downloading works without a separate file reader.
	if p.OnRequest == nil && p.HaveBF != nil {
		type readerAt interface {
			ReadAt([]byte, int64) (int, error)
		}
		if r, ok := w.(readerAt); ok {
			pieceLen := int64(p.TF.Info.PieceLength)
			p.OnRequest = func(index, begin, length uint32) []byte {
				if !p.HaveBF.HasPiece(int(index)) {
					return nil
				}
				block := make([]byte, length)
				offset := int64(index)*pieceLen + int64(begin)
				n, err := r.ReadAt(block, offset)
				if err != nil || n != int(length) {
					return nil
				}
				return block
			}
		}
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

	// Build piece list for remaining work (skipping both already-have and
	// skip-mask pieces — those must not be queued).
	var pieces []PieceWork
	for i, hash := range p.TF.Info.PieceHashes {
		if p.Have != nil && p.Have.HasPiece(i) {
			continue
		}
		if p.SkipMask != nil && p.SkipMask.HasPiece(i) {
			continue
		}
		length := p.TF.Info.PieceLength
		rem := tl - int64(i)*p.TF.Info.PieceLength
		if rem < length {
			length = rem
		}
		pieces = append(pieces, PieceWork{Index: i, Hash: hash, Length: int(length)})
	}
	remaining := len(pieces)

	// Initialize progress with already-completed pieces
	if alreadyDone > 0 {
		p.Progress.SetInitial(alreadyDone, tl, int64(p.TF.Info.PieceLength))
	}

	pq := NewPieceQueueWithOptions(pieces, p.SkipMask, numPieces, p.Cfg.PrioritizeFirstLast)
	if p.OnPieceQueue != nil {
		p.OnPieceQueue(pq)
	}
	resultCh, pm := runWorkers(ctx, peers, p.TF.InfoHash, p.PeerID, pq, p.PeerCh, p.PeerSink, p.Progress, p.Cfg, p.OnRequest, p.HaveBF, p.TF, p.WebSeedURLs)
	if p.OnPeerMgr != nil {
		p.OnPeerMgr(pm)
	}

	pieceLength := p.TF.Info.PieceLength
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
			offset := int64(res.Index) * pieceLength
			if _, err := w.WriteAt(res.Data, offset); err != nil {
				res.Free()
				return fmt.Errorf("download: write piece %d failed: %w", res.Index, err)
			}
			p.Progress.Add(len(res.Data))
			res.Free()
			// Update bitfield BEFORE notifying peers, so OnRequest
			// can serve the piece immediately when peers request it.
			if p.OnPiece != nil {
				p.OnPiece(res.Index)
			}
			pm.BroadcastHave(res.Index)
			completed++
		}
	}

	return nil
}

// runWorkers launches peer workers with rarest-first piece selection and dynamic peer injection.
// Returns result channel and PeerManager (for MsgHave broadcasting).
func runWorkers(ctx context.Context, initialPeers []tracker.Peer, infoHash, peerID [20]byte, pq *PieceQueue, peerCh <-chan []tracker.Peer, peerSink chan<- []tracker.Peer, progress *Progress, cfg DownloadConfig, onRequest func(uint32, uint32, uint32) []byte, haveBF peer.Bitfield, tf *torrent.TorrentFile, webSeedURLs []string) (<-chan PieceResult, *PeerManager) {
	resultCh := make(chan PieceResult, 64)

	pm := NewPeerManager(DefaultUnchokeSlots)
	go pm.Run(ctx)

	// Create piece buffer pool based on max piece size
	piecePool := newPiecePool(pq.maxPieceLen())

	sem := make(chan struct{}, cfg.MaxPeers)
	// Use shared global dial semaphore if provided, otherwise create per-torrent.
	dialSem := cfg.DialSem
	if dialSem == nil {
		maxDials := cfg.MaxPeers / 2
		if maxDials < 10 {
			maxDials = 10
		}
		dialSem = make(chan struct{}, maxDials)
	}
	seen := &sync.Map{}
	var preferEncrypt atomic.Bool

	// allSeen tracks every peer ever spawned for retry sweeps.
	// Cap at MaxPeers*20 to bound memory while covering large swarms.
	maxAllSeen := cfg.MaxPeers * 20
	if maxAllSeen < 5000 {
		maxAllSeen = 5000
	}
	var allSeenMu sync.Mutex
	var allSeenList []tracker.Peer
	allSeenSet := make(map[string]struct{})

	var activeWorkers sync.WaitGroup

	spawnWorker := func(p tracker.Peer) {
		addr := p.String()
		if _, loaded := seen.LoadOrStore(addr, true); loaded {
			return
		}
		allSeenMu.Lock()
		if _, exists := allSeenSet[addr]; !exists && len(allSeenList) < maxAllSeen {
			allSeenSet[addr] = struct{}{}
			allSeenList = append(allSeenList, p)
		}
		allSeenMu.Unlock()

		activeWorkers.Add(1)
		go func() {
			defer activeWorkers.Done()
			defer seen.Delete(addr)

			// Limit concurrent dial attempts to avoid NAT session exhaustion
			select {
			case <-ctx.Done():
				return
			case dialSem <- struct{}{}:
			}

			var client *Client
			var err error
			if cfg.Encryption {
				client, err = NewClientEncrypted(p, infoHash, peerID, cfg.DialTimeout, &preferEncrypt, cfg.EnableUTP, cfg.DisablePEX)
			} else {
				client, err = newClientFull(p, infoHash, peerID, cfg.DialTimeout, false, cfg.EnableUTP, cfg.DisablePEX)
			}
			<-dialSem // release dial slot after connection attempt

			if err != nil {
				slog.Debug("peer dial failed", "peer", addr, "err", err)
				return
			}

			// Acquire peer slot for established connection
			select {
			case <-ctx.Done():
				client.SetDisconnectReason("normal")
				_ = client.Close()
				return
			case sem <- struct{}{}:
				defer func() { <-sem }()
			}
			numPieces := len(tf.Info.PieceHashes)
			// Validate bitfield size to reject DoS-sized bitfields
			if err := client.ValidateBitfield(numPieces); err != nil {
				client.SetDisconnectReason("protocol_error: " + err.Error())
				_ = client.Close()
				return
			}
			client.maxPipeline = cfg.MaxPipeline
			client.piecePool = piecePool
			client.progress = progress
			client.numPieces = numPieces
			if peerSink != nil {
				client.PeerSink = peerSink
			}
			if onRequest != nil {
				client.OnRequest = onRequest
			}
			defer func() { _ = client.Close() }()

			// Tell the peer what pieces we have so they can request from us
			if haveBF != nil {
				_ = client.SendBitfield(haveBF)
			}

			pq.AddPeerBitfield(client.bitfield)
			defer pq.RemovePeerBitfield(client.bitfield)

			pm.Register(client)
			defer pm.Unregister(client)

			progress.PeerConnect()
			defer progress.PeerDisconnect()

			for {
				select {
				case <-ctx.Done():
					client.SetDisconnectReason("normal")
					return
				default:
				}

				pw, ok := pq.Pick(client.HasPiece)
				if !ok {
					select {
					case <-ctx.Done():
						client.SetDisconnectReason("normal")
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
					client.SetDisconnectReason(classifyDownloadError(err))
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
					client.SetDisconnectReason("normal")
					return
				}
			}
		}()
	}

	// retryPeers goroutine: re-attempt disconnected peers periodically.
	// Uses a conservative interval to avoid NAT session exhaustion.
	retryInterval := cfg.RetryInterval
	if retryInterval <= 0 {
		retryInterval = 60 * time.Second
	}
	retryDone := make(chan struct{})
	go func() {
		defer close(retryDone)
		timer := time.NewTimer(retryInterval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if pq.Remaining() == 0 {
					return
				}
				// Only retry if we have few active peers.
				// Spawn up to MaxPeers candidates per sweep to bound goroutine
				// pressure when allSeenList is large.
				if pm.PeerCount() < cfg.MaxPeers/2 {
					allSeenMu.Lock()
					snapshot := make([]tracker.Peer, len(allSeenList))
					copy(snapshot, allSeenList)
					allSeenMu.Unlock()
					limit := cfg.MaxPeers
					for _, p := range snapshot {
						if limit <= 0 {
							break
						}
						spawnWorker(p)
						limit--
					}
				}
				timer.Reset(retryInterval)
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

	return resultCh, pm
}

// classifyDownloadError maps a DownloadPiece error to a short reason tag
// used in the PeerDisconnected event payload. The mapping favours stable
// short tags ("timeout", "choked_out", "protocol_error: ...") so UI / log
// consumers can group related disconnects without parsing free-form text.
func classifyDownloadError(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	case strings.Contains(msg, "peer choked"):
		return "choked_out"
	case strings.Contains(msg, "hash mismatch"),
		strings.Contains(msg, "rejected piece"),
		strings.Contains(msg, "out of bounds"),
		strings.Contains(msg, "too many unexpected messages"):
		return "protocol_error: " + msg
	case strings.Contains(msg, "EOF"), strings.Contains(msg, "connection reset"), strings.Contains(msg, "broken pipe"):
		return "peer_closed"
	}
	return "unknown"
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
