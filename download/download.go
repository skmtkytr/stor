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
	// MaxPipeline is the number of outstanding requests per peer.
	MaxPipeline = 10
	// MaxPeers is the maximum number of concurrent peer connections.
	MaxPeers = 30
)

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
	choked         bool
	sentInterested bool
}

// NewClient connects to a peer, performs the handshake, and receives the bitfield.
func NewClient(p tracker.Peer, infoHash, peerID [20]byte) (*Client, error) {
	conn, err := net.DialTimeout("tcp", p.String(), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("download: connect to %s failed: %w", p, err)
	}

	closeOnErr := func() { _ = conn.Close() }

	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	hs := &peer.Handshake{InfoHash: infoHash, PeerID: peerID}
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
		conn:     conn,
		r:        bufio.NewReaderSize(conn, 64*1024),
		w:        bufio.NewWriterSize(conn, 32*1024),
		peerID:   peerID,
		infoHash: infoHash,
		choked:   true,
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

	return c, nil
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
		for backlog < MaxPipeline && requested < pw.Length {
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
			backlog--
		case peer.MsgChoke:
			c.choked = true
			return nil, fmt.Errorf("download: peer choked during piece %d", pw.Index)
		case peer.MsgHave:
			idx, err := peer.ParseHave(msg.Payload)
			if err == nil {
				c.bitfield.SetPiece(int(idx))
			}
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
	sem := make(chan struct{}, MaxPeers)

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

// DownloadToFileCtx is like DownloadToFile but accepts a context for cancellation.
// Progress is provided externally so the caller can read snapshots.
func DownloadToFileCtx(ctx context.Context, tf *torrent.TorrentFile, peerID [20]byte, peers []tracker.Peer, path string, progress *Progress) error {
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
	resultCh := startWorkersCtx(ctx, peers, tf.InfoHash, peerID, workCh, progress)

	pieceLength := int(tf.Info.PieceLength)
	for completed := 0; completed < numPieces; {
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
			progress.Add(len(res.Data))
			completed++
		}
	}

	return nil
}

// startWorkersCtx is like startWorkers but respects context cancellation.
func startWorkersCtx(ctx context.Context, peers []tracker.Peer, infoHash, peerID [20]byte, workCh chan PieceWork, progress *Progress) <-chan PieceResult {
	resultCh := make(chan PieceResult, cap(workCh))

	var wg sync.WaitGroup
	sem := make(chan struct{}, MaxPeers)

	for _, p := range peers {
		wg.Add(1)
		go func(p tracker.Peer) {
			defer wg.Done()

			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
				defer func() { <-sem }()
			}

			client, err := NewClient(p, infoHash, peerID)
			if err != nil {
				return
			}
			defer func() { _ = client.Close() }()

			progress.PeerConnect()
			defer progress.PeerDisconnect()

			for {
				select {
				case <-ctx.Done():
					return
				case pw, ok := <-workCh:
					if !ok {
						return
					}
					if !client.HasPiece(pw.Index) {
						workCh <- pw
						continue
					}

					data, err := client.DownloadPiece(pw)
					if err != nil {
						workCh <- pw
						return
					}

					select {
					case resultCh <- PieceResult{Index: pw.Index, Data: data}:
					case <-ctx.Done():
						return
					}
				}
			}
		}(p)
	}

	go func() {
		wg.Wait()
		close(workCh)
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
