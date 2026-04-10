package download

import (
	"bufio"
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

	// Send handshake (unbuffered, small and one-shot)
	hs := &peer.Handshake{InfoHash: infoHash, PeerID: peerID}
	if err := peer.WriteHandshake(conn, hs); err != nil {
		closeOnErr()
		return nil, fmt.Errorf("download: handshake write failed: %w", err)
	}

	// Read handshake
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
		r:        bufio.NewReaderSize(conn, 64*1024), // 64KB read buffer
		w:        bufio.NewWriterSize(conn, 32*1024), // 32KB write buffer
		peerID:   peerID,
		infoHash: infoHash,
		choked:   true,
	}

	// Read bitfield
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

// sendInterested sends an interested message if not already sent.
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

// waitForUnchoke reads messages until unchoked or error.
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

	// Download blocks with pipelining
	buf := make([]byte, pw.Length)
	downloaded := 0
	requested := 0
	backlog := 0

	for downloaded < pw.Length {
		// Fill pipeline — batch writes then flush once
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

		// Read response
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

	// Verify SHA1
	hash := sha1.Sum(buf)
	if hash != pw.Hash {
		return nil, fmt.Errorf("download: piece %d hash mismatch", pw.Index)
	}

	return buf, nil
}

// Download downloads all pieces of a torrent concurrently and writes to the output file.
func Download(tf *torrent.TorrentFile, peerID [20]byte, peers []tracker.Peer) ([]byte, error) {
	totalLength := tf.Info.Length
	if totalLength == 0 {
		for _, f := range tf.Info.Files {
			totalLength += f.Length
		}
	}

	numPieces := len(tf.Info.PieceHashes)

	// Deduplicate peers
	peers = deduplicatePeers(peers)

	// Work queue
	workCh := make(chan PieceWork, numPieces)
	for i, hash := range tf.Info.PieceHashes {
		length := int(tf.Info.PieceLength)
		remaining := int(totalLength) - i*int(tf.Info.PieceLength)
		if remaining < length {
			length = remaining
		}
		workCh <- PieceWork{Index: i, Hash: hash, Length: length}
	}

	resultCh := make(chan PieceResult, numPieces)

	var wg sync.WaitGroup
	sem := make(chan struct{}, MaxPeers)

	for _, p := range peers {
		wg.Add(1)
		go func(p tracker.Peer) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			client, err := NewClient(p, tf.InfoHash, peerID)
			if err != nil {
				return
			}
			defer func() { _ = client.Close() }()

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

	// Collect results
	progress := NewProgress(numPieces, totalLength)
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

	// Assemble
	buf := make([]byte, 0, totalLength)
	for _, data := range results {
		buf = append(buf, data...)
	}
	return buf, nil
}

// DownloadToFile downloads all pieces concurrently and writes directly to a file.
// Avoids holding all data in memory.
func DownloadToFile(tf *torrent.TorrentFile, peerID [20]byte, peers []tracker.Peer, path string) error {
	totalLength := tf.Info.Length
	if totalLength == 0 {
		for _, f := range tf.Info.Files {
			totalLength += f.Length
		}
	}

	numPieces := len(tf.Info.PieceHashes)

	// Create output file, preallocate
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := f.Truncate(totalLength); err != nil {
		return fmt.Errorf("download: truncate failed: %w", err)
	}

	// Deduplicate peers
	peers = deduplicatePeers(peers)

	// Work queue
	workCh := make(chan PieceWork, numPieces)
	for i, hash := range tf.Info.PieceHashes {
		length := int(tf.Info.PieceLength)
		remaining := int(totalLength) - i*int(tf.Info.PieceLength)
		if remaining < length {
			length = remaining
		}
		workCh <- PieceWork{Index: i, Hash: hash, Length: length}
	}

	// Result: just index + data, we write to file from the collector goroutine
	type writeResult struct {
		index int
		data  []byte
	}
	resultCh := make(chan writeResult, numPieces)

	var wg sync.WaitGroup
	sem := make(chan struct{}, MaxPeers)

	for _, p := range peers {
		wg.Add(1)
		go func(p tracker.Peer) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			client, err := NewClient(p, tf.InfoHash, peerID)
			if err != nil {
				return
			}
			defer func() { _ = client.Close() }()

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

				resultCh <- writeResult{index: pw.Index, data: data}
			}
		}(p)
	}

	go func() {
		wg.Wait()
		close(workCh)
	}()

	progress := NewProgress(numPieces, totalLength)
	pieceLength := int(tf.Info.PieceLength)
	for completed := 0; completed < numPieces; {
		select {
		case res := <-resultCh:
			offset := int64(res.index) * int64(pieceLength)
			if _, err := f.WriteAt(res.data, offset); err != nil {
				return fmt.Errorf("download: write piece %d failed: %w", res.index, err)
			}
			progress.Add(len(res.data))
			completed++
			fmt.Print(progress)
		case <-time.After(2 * time.Minute):
			return fmt.Errorf("download: timed out at %d/%d pieces", completed, numPieces)
		}
	}
	fmt.Println()

	return nil
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
