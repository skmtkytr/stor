package download

import (
	"crypto/sha1"
	"fmt"
	"net"
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
	MaxPipeline = 5
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

// Client represents a connection to a single peer.
type Client struct {
	conn     net.Conn
	peerID   [20]byte
	infoHash [20]byte
	bitfield peer.Bitfield
	choked   bool
}

// NewClient connects to a peer, performs the handshake, and receives the bitfield.
func NewClient(p tracker.Peer, infoHash, peerID [20]byte) (*Client, error) {
	conn, err := net.DialTimeout("tcp", p.String(), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("download: connect to %s failed: %w", p, err)
	}

	closeOnErr := func() { _ = conn.Close() }

	// Set deadline for entire handshake phase
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	// Send handshake
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

	// Read bitfield (first message should be bitfield)
	c := &Client{
		conn:     conn,
		peerID:   peerID,
		infoHash: infoHash,
		choked:   true,
	}

	msg, err := peer.ReadMessage(conn)
	_ = conn.SetDeadline(time.Time{}) // clear deadline
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

// DownloadPiece downloads a single piece from the peer.
func (c *Client) DownloadPiece(pw PieceWork) ([]byte, error) {
	_ = c.conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()

	// Send interested
	msg := &peer.Message{ID: peer.MsgInterested}
	if err := msg.Write(c.conn); err != nil {
		return nil, err
	}

	// Wait for unchoke
	for c.choked {
		msg, err := peer.ReadMessage(c.conn)
		if err != nil {
			return nil, err
		}
		if msg == nil {
			continue // keep-alive
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

	// Download blocks
	buf := make([]byte, pw.Length)
	downloaded := 0
	requested := 0
	backlog := 0

	for downloaded < pw.Length {
		// Pipeline requests
		for backlog < MaxPipeline && requested < pw.Length {
			blockSize := BlockSize
			if requested+blockSize > pw.Length {
				blockSize = pw.Length - requested
			}
			req := peer.NewRequestMessage(uint32(pw.Index), uint32(requested), uint32(blockSize))
			if err := req.Write(c.conn); err != nil {
				return nil, err
			}
			requested += blockSize
			backlog++
		}

		// Read response
		msg, err := peer.ReadMessage(c.conn)
		if err != nil {
			return nil, err
		}
		if msg == nil {
			continue // keep-alive
		}

		switch msg.ID {
		case peer.MsgPiece:
			idx, begin, block, err := peer.ParsePiece(msg.Payload)
			if err != nil {
				return nil, err
			}
			if int(idx) != pw.Index {
				continue // wrong piece
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

// Download downloads all pieces of a torrent concurrently and returns the assembled data.
func Download(tf *torrent.TorrentFile, peerID [20]byte, peers []tracker.Peer) ([]byte, error) {
	totalLength := tf.Info.Length
	if totalLength == 0 {
		for _, f := range tf.Info.Files {
			totalLength += f.Length
		}
	}

	numPieces := len(tf.Info.PieceHashes)

	// Work queue: pieces to download
	workCh := make(chan PieceWork, numPieces)
	for i, hash := range tf.Info.PieceHashes {
		length := int(tf.Info.PieceLength)
		remaining := int(totalLength) - i*int(tf.Info.PieceLength)
		if remaining < length {
			length = remaining
		}
		workCh <- PieceWork{Index: i, Hash: hash, Length: length}
	}

	// Result channel
	resultCh := make(chan PieceResult, numPieces)

	// Launch worker per peer (up to MaxPeers)
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
					// Put it back for another peer
					workCh <- pw
					continue
				}

				data, err := client.DownloadPiece(pw)
				if err != nil {
					// Put it back and stop this peer
					workCh <- pw
					return
				}

				resultCh <- PieceResult{Index: pw.Index, Data: data}
			}
		}(p)
	}

	// Close work channel when all workers are done
	go func() {
		wg.Wait()
		close(workCh)
	}()

	// Collect results
	results := make([][]byte, numPieces)
	for completed := 0; completed < numPieces; {
		select {
		case res := <-resultCh:
			results[res.Index] = res.Data
			completed++
			fmt.Printf("\rpiece %d/%d downloaded", completed, numPieces)
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
