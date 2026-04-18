package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/skmtkytr/stor/bencode"
	"github.com/skmtkytr/stor/daemon"
	dhtpkg "github.com/skmtkytr/stor/dht"
	"github.com/skmtkytr/stor/download"
	"github.com/skmtkytr/stor/engine"
	"github.com/skmtkytr/stor/magnet"
	"github.com/skmtkytr/stor/peer"
	"github.com/skmtkytr/stor/storage"
	"github.com/skmtkytr/stor/torrent"
	"github.com/skmtkytr/stor/tracker"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "daemon":
		runDaemon()
	case "help", "--help", "-h":
		printUsage()
	default:
		runOneShot()
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `stor - BitTorrent client

Usage:
  stor daemon [options]       Start the daemon
  stor <torrent|magnet> [dir] One-shot download

Daemon options:
  --port PORT       Listen port (default: 9090)
  --dir DIR         Download directory (default: ~/Downloads)
  --config PATH     Config file path (default: ~/.config/stor/config.toml)
`)
}

// --- Daemon mode ---

func runDaemon() {
	// Parse daemon flags
	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, ".config", "stor", "config.toml")
	port := 0
	dir := ""

	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			if i+1 < len(args) {
				i++
				_, _ = fmt.Sscanf(args[i], "%d", &port)
			}
		case "--dir":
			if i+1 < len(args) {
				i++
				dir = args[i]
			}
		case "--config":
			if i+1 < len(args) {
				i++
				configPath = args[i]
			}
		}
	}

	cfg, err := daemon.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// CLI flags override config
	if port > 0 {
		cfg.Port = port
	}
	if dir != "" {
		cfg.DownloadDir = dir
	}

	// Initialize structured logging
	logLevel := daemon.ParseLogLevel(cfg.LogLevel)
	logHandler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
	slog.SetDefault(slog.New(logHandler))

	slog.Info("config loaded",
		"path", configPath,
		"port", cfg.Port,
		"download_dir", cfg.DownloadDir,
		"log_level", cfg.LogLevel,
		"max_active", cfg.MaxActive,
	)

	peerPort := uint16(6881)
	if cfg.PeerPort > 0 && cfg.PeerPort < 65536 {
		peerPort = uint16(cfg.PeerPort)
	}
	engCfg := engine.Config{
		DownloadDir: cfg.DownloadDir,
		TmpDir:      cfg.TmpDir,
		StatePath:   cfg.StatePath,
		ListenPort:  peerPort,
		MaxActive:   cfg.MaxActive,
		MaxPeers:    cfg.MaxPeers,
		MaxPipeline: cfg.MaxPipeline,
		DialTimeout: cfg.DialTimeout,
		NumWant:     cfg.NumWant,
		DHTAlpha:    cfg.DHTAlpha,
		EnableUTP:   cfg.EnableUTP,
	}

	eng, err := engine.New(engCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine error: %v\n", err)
		os.Exit(1)
	}

	if err := eng.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "engine start error: %v\n", err)
		os.Exit(1)
	}

	// PID file: kill old daemon if running, write our PID
	pidPath := filepath.Join(filepath.Dir(configPath), "stor.pid")
	if err := daemon.AcquirePIDFile(pidPath); err != nil {
		fmt.Fprintf(os.Stderr, "pid file error: %v\n", err)
		os.Exit(1)
	}

	d := daemon.New(eng, cfg)

	slog.Info("daemon starting",
		"pid", os.Getpid(),
		"listen", fmt.Sprintf("0.0.0.0:%d", cfg.Port),
		"download_dir", cfg.DownloadDir,
		"web_ui", fmt.Sprintf("http://localhost:%d/", cfg.Port),
	)

	fmt.Printf("stor daemon (PID %d)\n", os.Getpid())
	fmt.Printf("  API Key:      %s\n", cfg.APIKey)
	fmt.Printf("  Listen:       0.0.0.0:%d\n", cfg.Port)
	fmt.Printf("  Download dir: %s\n", cfg.DownloadDir)
	fmt.Printf("  Config:       %s\n", configPath)
	fmt.Printf("  Web UI:       http://localhost:%d/\n", cfg.Port)

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := d.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("daemon listen failed", "error", err)
			os.Exit(1)
		}
	}()

	sig := <-sigCh
	slog.Info("shutdown signal received", "signal", sig.String())
	_ = d.Stop()
	_ = eng.Stop()
	daemon.ReleasePIDFile(pidPath)
	slog.Info("daemon stopped")
}

// --- One-shot download mode (legacy) ---

func runOneShot() {
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

	peers, err := announceToTrackers(tf, peerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tracker error: %v\n", err)
		os.Exit(1)
	}

	outPath := filepath.Join(outputDir, tf.Info.Name)
	fmt.Printf("\nDownloading to %s...\n", outPath)
	if err := download.DownloadToFile(tf, peerID, peers, outPath); err != nil {
		fmt.Fprintf(os.Stderr, "download error: %v\n", err)
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
				IPv6:        tracker.LocalIPv6(),
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

	tf, err := fetchMetadataConcurrent(allPeers, m.InfoHash, peerID)
	if err != nil {
		return nil, err
	}

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

func fetchMetadataConcurrent(peers []tracker.Peer, infoHash, peerID [20]byte) (*torrent.TorrentFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type result struct {
		tf  *torrent.TorrentFile
		err error
	}

	resultCh := make(chan result, 1)
	sem := make(chan struct{}, 20)

	var wg sync.WaitGroup
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

			tf, err := fetchMetadataFromPeer(ctx, p, infoHash, peerID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  peer %s: %v\n", p, err)
				return
			}

			select {
			case resultCh <- result{tf: tf}:
				cancel()
			default:
			}
		}(p)
	}

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
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", p.String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

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

	metadata, err := magnet.FetchMetadata(extConn, int(peerHS.MetadataSize), infoHash)
	if err != nil {
		return nil, err
	}

	fmt.Printf("  peer %s: metadata fetched (%d bytes)\n", p, len(metadata))

	decoded, err := bencode.Decode(metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to decode metadata: %w", err)
	}
	infoDict, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("metadata is not a dict")
	}

	fullDict := map[string]any{"info": infoDict}
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

var dhtBootstrapNodes = []string{
	"router.bittorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"router.utorrent.com:6881",
	"dht.libtorrent.org:25401",
}

func announceToTrackers(tf *torrent.TorrentFile, peerID [20]byte) ([]tracker.Peer, error) {
	var allPeers []tracker.Peer
	var mu sync.Mutex
	var wg sync.WaitGroup

	trackers := tf.TrackerURLs()

	fmt.Printf("\nContacting %d tracker(s) + DHT...\n", len(trackers))

	for _, tr := range trackers {
		wg.Add(1)
		go func(tr string) {
			defer wg.Done()
			tl := storage.TotalSize(tf)
			req := tracker.AnnounceRequest{
				AnnounceURL: tr,
				InfoHash:    tf.InfoHash,
				PeerID:      peerID,
				Port:        6881,
				Left:        tl,
				Event:       tracker.EventStarted,
				IPv6:        tracker.LocalIPv6(),
			}
			resp, err := tracker.Announce(req)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  tracker %s: %v\n", tr, err)
				return
			}
			fmt.Printf("  tracker %s: %d peers\n", tr, len(resp.Peers))
			mu.Lock()
			allPeers = append(allPeers, resp.Peers...)
			mu.Unlock()
		}(tr)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		d, err := dhtpkg.New(":0")
		if err != nil {
			return
		}
		defer func() { _ = d.Close() }()

		if err := d.Bootstrap(dhtBootstrapNodes); err != nil {
			return
		}
		fmt.Printf("  DHT: bootstrapped (%d nodes)\n", d.TableSize())

		peerAddrs, err := d.GetPeers(tf.InfoHash)
		if err != nil {
			return
		}

		var dhtPeers []tracker.Peer
		for _, addr := range peerAddrs {
			host, portStr, splitErr := net.SplitHostPort(addr)
			if splitErr != nil {
				continue
			}
			ip := net.ParseIP(host)
			if ip == nil {
				continue
			}
			var port uint16
			if _, scanErr := fmt.Sscanf(portStr, "%d", &port); scanErr == nil {
				dhtPeers = append(dhtPeers, tracker.Peer{IP: ip, Port: port})
			}
		}

		if len(dhtPeers) > 0 {
			fmt.Printf("  DHT: %d peers\n", len(dhtPeers))
			mu.Lock()
			allPeers = append(allPeers, dhtPeers...)
			mu.Unlock()
		}
	}()

	wg.Wait()

	if len(allPeers) == 0 {
		return nil, fmt.Errorf("no peers found from any tracker or DHT")
	}

	fmt.Printf("Total: %d peers\n", len(allPeers))
	return allPeers, nil
}
