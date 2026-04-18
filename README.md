# stor

[![Go Reference](https://pkg.go.dev/badge/github.com/skmtkytr/stor.svg)](https://pkg.go.dev/github.com/skmtkytr/stor)
[![Go Report Card](https://goreportcard.com/badge/github.com/skmtkytr/stor?v=1)](https://goreportcard.com/report/github.com/skmtkytr/stor)
[![Release](https://img.shields.io/github/v/release/skmtkytr/stor)](https://github.com/skmtkytr/stor/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Docker Image](https://img.shields.io/badge/docker-ghcr.io%2Fskmtkytr%2Fstor-blue?logo=docker)](https://ghcr.io/skmtkytr/stor)

[日本語](README.ja.md)

**Zero-dependency BitTorrent in a single binary.** Daemon, Web UI, and 14 BEPs — from bencoding to uTP — implemented from scratch in Go with no third-party torrent libraries.

## Why

Most Go BitTorrent implementations wrap anacrolix/torrent or similar. stor replaces a Deluge + WebUI + browser extension stack with one `go build` and one 9 MB Docker image.

## Protocol Support

| BEP | Spec | Status |
|-----|------|--------|
| 3 | The BitTorrent Protocol | Implemented (choking, rarest-first, endgame) |
| 5 | DHT Protocol | Implemented (shared instance, configurable alpha) |
| 6 | Fast Extension | Implemented (have-all, have-none, reject) |
| 7 | IPv6 Tracker Extension | Implemented (peers6, ipv6= announce param) |
| 9 | Extension for Peers to Send Metadata Files | Implemented |
| 10 | Extension Protocol | Implemented |
| 11 | Peer Exchange (PEX) | Implemented (IPv4 + IPv6 via added6/dropped6) |
| 12 | Multitracker Metadata Extension | Implemented |
| 15 | UDP Tracker Protocol | Implemented |
| 19 | WebSeed (GetRight) | Implemented (HTTP Range, multi-file) |
| 23 | Tracker Returns Compact Peer Lists | Implemented |
| 27 | Private Torrents | Implemented (DHT/PEX auto-disable) |
| 29 | uTP (Micro Transport Protocol) | Implemented (LEDBAT congestion control) |
| 52 | BitTorrent v2 | Partial (hybrid parse + v1 download) |
| — | MSE/PE (Protocol Encryption) | Implemented (768-bit DH + RC4, plaintext fallback) |

See [BEP.md](BEP.md) for detailed implementation notes and protocol format references.

## Features

- **Download**: rarest-first piece selection, endgame mode, resume with piece verification, multi-file support
- **Upload**: seeding after download, incoming peer listener, BEP 3 upload choking algorithm
- **Peers**: dynamic peer injection, periodic tracker re-announce, peer reconnection, PEX, multitracker
- **Transport**: TCP and uTP (LEDBAT), MSE/PE encryption with RC4, automatic plaintext fallback
- **Daemon**: JSON-RPC 2.0 API, persistent queue, configurable tuning, web-based settings
- **Web UI**: Deluge-inspired layout, real-time stats, filter/sort, settings editor
- **Chrome Extension**: intercepts `.torrent`/`magnet:` links, popup management
- **Docker**: multi-arch (amd64/arm64), ~9 MB distroless image

## Installation

### Go

```sh
go install github.com/skmtkytr/stor/cmd/stor@latest
```

### GitHub Releases

Download a pre-built binary from the [Releases](https://github.com/skmtkytr/stor/releases/latest) page.

### Build from Source

```sh
make && ./stor daemon
```

## Getting Started

```sh
stor daemon
```

On first launch, a config file is created at `~/.config/stor/config.toml` with a generated API key. The web UI is served at `http://localhost:9090`.

### Docker

```yaml
services:
  stor:
    image: ghcr.io/skmtkytr/stor:latest
    user: "1000:1000"
    ports:
      - "9090:9090"
      - "6881:6881"
      - "6881:6881/udp"
    volumes:
      - ./config:/config
      - ./data:/data
    restart: unless-stopped
```

### Standalone Download

```sh
./stor magnet:?xt=urn:btih:...
./stor path/to/file.torrent [output-dir]
```

## Configuration

All parameters in `config.toml` are optional. Shown values are defaults.

```toml
port = 9090
download_dir = "~/Downloads"
tmp_dir = ""          # temp dir for in-progress downloads (empty = use download_dir)
max_active = 5
log_level = "info"    # debug, info, warn, error

# Peer and tracker tuning
max_peers = 100       # concurrent connections per torrent
max_pipeline = 16     # outstanding block requests per peer
dial_timeout = 3      # seconds
numwant = 200         # peers requested per tracker announce
dht_alpha = 8         # DHT lookup concurrency
enable_utp = false    # enable uTP transport (LEDBAT congestion control)
```

Most settings can be changed at runtime via the Web UI settings page or the `daemon.setConfig` RPC method.

## API

The daemon exposes a JSON-RPC 2.0 endpoint at `POST /api/rpc`. Authentication uses `Authorization: Bearer <api_key>`.

| Method | Description |
|--------|-------------|
| `torrent.add` | Add torrent by magnet URI or .torrent file path |
| `torrent.addFile` | Add torrent from base64-encoded .torrent data |
| `torrent.addURL` | Add torrent from HTTP URL (SSRF-protected) |
| `torrent.remove` | Remove a torrent by ID |
| `torrent.pause` | Pause a torrent |
| `torrent.resume` | Resume a paused torrent |
| `torrent.get` | Get torrent details by ID |
| `torrent.list` | List all torrents with stats |
| `torrent.queueTop` | Move torrent to top of queue |
| `torrent.queueUp` | Move torrent up one position |
| `torrent.queueDown` | Move torrent down one position |
| `torrent.queueBottom` | Move torrent to bottom of queue |
| `daemon.stats` | Global stats (speeds, peer count, DHT size) |
| `daemon.setMaxActive` | Change max concurrent downloads |
| `daemon.setConfig` | Update runtime configuration |
| `daemon.version` | Return version string |

Additional REST endpoints:

| Endpoint | Description |
|----------|-------------|
| `POST /api/add` | Form-based add (for Chrome extension) |
| `GET /api/torrents` | Torrent list (polling) |
| `GET /` | Web UI |

## Security

stor applies defense-in-depth against malicious peers, trackers, and torrents:

| Layer | Protection |
|-------|------------|
| **Input parsing** | Bencode limits: 64 MB strings, 1M items/collection, 2M total items, depth 100 |
| **Metadata** | 32 MB max metadata size, SHA1 info hash verification |
| **Tracker** | 10 MB response limit, 10K peers/response cap, DNS pinning (anti-rebinding) |
| **Peers** | Bitfield size validation, block bounds checking, piece hash verification |
| **Upload** | 32 KiB max block size, piece index validation, offset bounds checking |
| **Network** | Private IP filtering (loopback/RFC 1918/link-local) on tracker and DHT results |
| **DHT** | 1K node / 5K peer lookup limits, token-based announce, pending transaction TTL |
| **Transport** | uTP connection limit (4096), incoming peer limit (200), dial semaphore (50) |
| **Filesystem** | Path traversal prevention, symlink resolution, torrent name sanitization |
| **Daemon** | API key auth, request size limits, PID file management, CORS origin reflection |
| **Encryption** | 768-bit DH key exchange, RC4 with 1024-byte keystream discard |

## Chrome Extension

Load `extension/` as an unpacked extension. Features:

- Automatic interception of `.torrent` and `magnet:` links
- Context menu integration
- Popup with torrent list and inline controls
- Configurable per-option with connection test

## Project Structure

```
cmd/stor/       Entry point — daemon and standalone modes
daemon/         HTTP server, JSON-RPC 2.0 API, configuration
engine/         Torrent lifecycle, queue scheduling, parallel magnet resolution
download/       Piece-level I/O, rarest-first, endgame, peer manager, choking
storage/        Multi-file writer, piece verification, file handle caching
peer/           Wire protocol, BEP 6/10/11 extensions, PEX
tracker/        HTTP and UDP announce clients (DNS-pinned)
dht/            Kademlia routing table, iterative lookup, get_peers
magnet/         Magnet URI parsing, BEP 9 metadata fetch
torrent/        .torrent file parser (v1 + hybrid v2)
bencode/        Bencoding codec (DoS-hardened)
mse/            Message Stream Encryption (768-bit DH + RC4)
utp/            Micro Transport Protocol (LEDBAT congestion control)
ui/             SvelteKit SPA, embedded in the binary at build time
extension/      Chrome extension, Manifest V3
```

## License

MIT
