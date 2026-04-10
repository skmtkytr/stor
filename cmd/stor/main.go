package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"github.com/skmtkytr/stor/download"
	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/tracker"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: stor <torrent-file> [output-dir]\n")
		os.Exit(1)
	}

	torrentPath := os.Args[1]
	outputDir := "."
	if len(os.Args) >= 3 {
		outputDir = os.Args[2]
	}

	// Read .torrent file
	data, err := os.ReadFile(torrentPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Parse
	tf, err := torrent.Parse(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Name:         %s\n", tf.Info.Name)
	fmt.Printf("Tracker:      %s\n", tf.Announce)
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

	// Generate peer ID: -ST0001- + 12 random bytes
	var peerID [20]byte
	copy(peerID[:], "-ST0001-")
	rand.Read(peerID[8:])

	// Announce to tracker
	fmt.Println("\nContacting tracker...")
	req := tracker.AnnounceRequest{
		AnnounceURL: tf.Announce,
		InfoHash:    tf.InfoHash,
		PeerID:      peerID,
		Port:        6881,
		Left:        tf.Info.Length,
		Event:       tracker.EventStarted,
	}

	resp, err := tracker.Announce(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tracker error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Got %d peers (interval: %ds)\n", len(resp.Peers), resp.Interval)

	// Download
	fmt.Println("\nDownloading...")
	content, err := download.Download(tf, peerID, resp.Peers)
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
