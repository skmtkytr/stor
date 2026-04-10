package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
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

	// Try to get peers from trackers in the magnet link
	var peers []tracker.Peer
	for _, tr := range m.Trackers {
		req := tracker.AnnounceRequest{
			AnnounceURL: tr,
			InfoHash:    m.InfoHash,
			PeerID:      peerID,
			Port:        6881,
			Left:        1, // We don't know the size yet
			Event:       tracker.EventStarted,
		}
		resp, err := tracker.Announce(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tracker %s: %v\n", tr, err)
			continue
		}
		peers = append(peers, resp.Peers...)
	}

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
			peers = append(peers, tracker.Peer{IP: ip, Port: port})
		}
	}

	if len(peers) == 0 {
		return nil, fmt.Errorf("no peers found for magnet link")
	}

	fmt.Printf("Found %d peers, fetching metadata...\n", len(peers))

	// Try to fetch metadata from each peer
	for _, p := range peers {
		tf, err := fetchMetadataFromPeer(p, m.InfoHash, peerID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  peer %s: %v\n", p, err)
			continue
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

	return nil, fmt.Errorf("failed to fetch metadata from any peer")
}

func fetchMetadataFromPeer(p tracker.Peer, infoHash, peerID [20]byte) (*torrent.TorrentFile, error) {
	conn, err := net.DialTimeout("tcp", p.String(), 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

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
			continue
		}
		fmt.Printf("  %s: %d peers\n", tr, len(resp.Peers))
		allPeers = append(allPeers, resp.Peers...)
	}

	if len(allPeers) == 0 {
		return nil, fmt.Errorf("no peers found from any tracker")
	}

	fmt.Printf("Total: %d peers\n", len(allPeers))
	return allPeers, nil
}
