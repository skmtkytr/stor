package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/skmtkytr/stor/bencode"
	"github.com/skmtkytr/stor/download"
	"github.com/skmtkytr/stor/magnet"
	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/tracker"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: stor <torrent-file|magnet-uri> [output-dir]\n")
		os.Exit(1)
	}

	input := os.Args[1]
	outputDir := "."
	if len(os.Args) >= 3 {
		outputDir = os.Args[2]
	}

	peerID := generatePeerID()

	var tf *torrent.TorrentFile
	var err error

	if strings.HasPrefix(input, "magnet:") {
		tf, err = handleMagnet(input, peerID)
	} else {
		tf, err = handleTorrentFile(input)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	printTorrentInfo(tf)

	// Announce to tracker
	peers, err := announceToTrackers(tf, peerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tracker error: %v\n", err)
		os.Exit(1)
	}

	// Download
	fmt.Println("\nDownloading...")
	content, err := download.Download(tf, peerID, peers)
	if err != nil {
		fmt.Fprintf(os.Stderr, "download error: %v\n", err)
		os.Exit(1)
	}

	// Write output
	outPath := filepath.Join(outputDir, tf.Info.Name)
	if err := os.WriteFile(outPath, content, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nDone! Saved to %s\n", outPath)
}

func generatePeerID() [20]byte {
	var id [20]byte
	copy(id[:], "-ST0001-")
	_, _ = rand.Read(id[8:])
	return id
}

func handleTorrentFile(path string) (*torrent.TorrentFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return torrent.Parse(data)
}

func handleMagnet(uri string, peerID [20]byte) (*torrent.TorrentFile, error) {
	m, err := magnet.Parse(uri)
	if err != nil {
		return nil, err
	}

	fmt.Printf("Magnet:    %x\n", m.InfoHash)
	if m.DisplayName != "" {
		fmt.Printf("Name:      %s\n", m.DisplayName)
	}
	fmt.Printf("Trackers:  %d\n", len(m.Trackers))

	// Get peers from trackers concurrently
	var allPeers []tracker.Peer
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, tr := range m.Trackers {
		wg.Add(1)
		go func(tr string) {
			defer wg.Done()
			req := tracker.AnnounceRequest{
				AnnounceURL: tr,
				InfoHash:    m.InfoHash,
				PeerID:      peerID,
				Port:        6881,
				Left:        1,
				Event:       tracker.EventStarted,
			}
			resp, err := tracker.Announce(req)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  tracker %s: %v\n", tr, err)
				return
			}
			mu.Lock()
			allPeers = append(allPeers, resp.Peers...)
			mu.Unlock()
			fmt.Printf("  %s: %d peers\n", tr, len(resp.Peers))
		}(tr)
	}
	wg.Wait()

	// Also add peers from x.pe in the magnet URI
	for _, pe := range m.Peers {
		host, portStr, err := net.SplitHostPort(pe)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		var port uint16
		if _, err := fmt.Sscanf(portStr, "%d", &port); err == nil {
			allPeers = append(allPeers, tracker.Peer{IP: ip, Port: port})
		}
	}

	if len(allPeers) == 0 {
		return nil, fmt.Errorf("no peers found for magnet link")
	}

	fmt.Printf("Found %d peers, fetching metadata...\n", len(allPeers))

	// Try to fetch metadata from peers concurrently
	tf, err := fetchMetadataConcurrent(allPeers, m.InfoHash, peerID)
	if err != nil {
		return nil, err
	}

	// Restore announce URLs from magnet
	if len(m.Trackers) > 0 {
		tf.Announce = m.Trackers[0]
		tiers := make([][]string, len(m.Trackers))
		for i, tr := range m.Trackers {
			tiers[i] = []string{tr}
		}
		tf.AnnounceList = tiers
	}
	return tf, nil
}

// fetchMetadataConcurrent tries multiple peers concurrently and returns
// as soon as one succeeds.
func fetchMetadataConcurrent(peers []tracker.Peer, infoHash, peerID [20]byte) (*torrent.TorrentFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type result struct {
		tf  *torrent.TorrentFile
		err error
	}

	resultCh := make(chan result, 1)

	// Limit concurrency to avoid opening too many connections
	sem := make(chan struct{}, 20)

	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Add(1)
		go func(p tracker.Peer) {
			defer wg.Done()

			// Check if already cancelled
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
				defer func() { <-sem }()
			}

			tf, err := fetchMetadataFromPeer(ctx, p, infoHash, peerID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  peer %s: %v\n", p, err)
				return
			}

			// First success wins
			select {
			case resultCh <- result{tf: tf}:
				cancel() // cancel all other goroutines
			default:
			}
		}(p)
	}

	// Close result channel when all goroutines done
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	res, ok := <-resultCh
	if !ok {
		return nil, fmt.Errorf("failed to fetch metadata from any peer")
	}
	return res.tf, res.err
}

func fetchMetadataFromPeer(ctx context.Context, p tracker.Peer, infoHash, peerID [20]byte) (*torrent.TorrentFile, error) {
	// Check cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Connect with short timeout
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", p.String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	// Set overall deadline for the entire metadata fetch
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// Handshake with extension support
	hs := &peer.Handshake{InfoHash: infoHash, PeerID: peerID, Extensions: true}
	if err := peer.WriteHandshake(conn, hs); err != nil {
		return nil, err
	}

	resp, err := peer.ReadHandshake(conn)
	if err != nil {
		return nil, err
	}
	if resp.InfoHash != infoHash {
		return nil, fmt.Errorf("info hash mismatch")
	}
	if !resp.Extensions {
		return nil, fmt.Errorf("peer does not support extensions")
	}

	// Negotiate ut_metadata extension
	ourHS := &peer.ExtHandshake{
		M: map[string]int64{"ut_metadata": 1},
		V: "stor/0.1.0",
	}
	extConn, peerHS, err := peer.NegotiateExtension(
		conn.(io.ReadWriter), "ut_metadata", 1, ourHS,
	)
	if err != nil {
		return nil, err
	}

	if peerHS.MetadataSize <= 0 {
		return nil, fmt.Errorf("peer reported no metadata")
	}

	// Fetch raw metadata
	metadata, err := magnet.FetchMetadata(extConn, int(peerHS.MetadataSize), infoHash)
	if err != nil {
		return nil, err
	}

	fmt.Printf("  peer %s: metadata fetched (%d bytes)\n", p, len(metadata))

	// Parse the metadata as a torrent info dict
	decoded, err := bencode.Decode(metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to decode metadata: %w", err)
	}
	infoDict, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("metadata is not a dict")
	}

	// Reconstruct a minimal .torrent structure
	fullDict := map[string]any{
		"info": infoDict,
	}
	fullData, err := bencode.Encode(fullDict)
	if err != nil {
		return nil, err
	}

	return torrent.Parse(fullData)
}

func printTorrentInfo(tf *torrent.TorrentFile) {
	fmt.Printf("Name:         %s\n", tf.Info.Name)
	if tf.Announce != "" {
		fmt.Printf("Tracker:      %s\n", tf.Announce)
	}
	fmt.Printf("Info Hash:    %x\n", tf.InfoHash)
	fmt.Printf("Piece Length: %d bytes\n", tf.Info.PieceLength)
	fmt.Printf("Pieces:       %d\n", len(tf.Info.PieceHashes))

	if tf.Info.Length > 0 {
		fmt.Printf("Size:         %d bytes\n", tf.Info.Length)
	} else {
		var total int64
		for _, f := range tf.Info.Files {
			total += f.Length
		}
		fmt.Printf("Files:        %d (%d bytes total)\n", len(tf.Info.Files), total)
	}
}

func announceToTrackers(tf *torrent.TorrentFile, peerID [20]byte) ([]tracker.Peer, error) {
	var allPeers []tracker.Peer
	var mu sync.Mutex
	var wg sync.WaitGroup

	trackers := []string{}
	if tf.Announce != "" {
		trackers = append(trackers, tf.Announce)
	}
	for _, tier := range tf.AnnounceList {
		trackers = append(trackers, tier...)
	}

	// Deduplicate
	seen := map[string]bool{}
	unique := trackers[:0]
	for _, tr := range trackers {
		if !seen[tr] {
			seen[tr] = true
			unique = append(unique, tr)
		}
	}
	trackers = unique

	fmt.Printf("\nContacting %d tracker(s)...\n", len(trackers))

	for _, tr := range trackers {
		wg.Add(1)
		go func(tr string) {
			defer wg.Done()
			totalLength := tf.Info.Length
			if totalLength == 0 {
				for _, f := range tf.Info.Files {
					totalLength += f.Length
				}
			}

			req := tracker.AnnounceRequest{
				AnnounceURL: tr,
				InfoHash:    tf.InfoHash,
				PeerID:      peerID,
				Port:        6881,
				Left:        totalLength,
				Event:       tracker.EventStarted,
			}

			resp, err := tracker.Announce(req)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  tracker %s: %v\n", tr, err)
				return
			}
			fmt.Printf("  %s: %d peers\n", tr, len(resp.Peers))
			mu.Lock()
			allPeers = append(allPeers, resp.Peers...)
			mu.Unlock()
		}(tr)
	}
	wg.Wait()

	if len(allPeers) == 0 {
		return nil, fmt.Errorf("no peers found from any tracker")
	}

	fmt.Printf("Total: %d peers\n", len(allPeers))
	return allPeers, nil
}
