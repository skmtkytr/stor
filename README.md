# stor

[日本語](README.ja.md)

A BitTorrent client built from the ground up in Go — no third-party torrent libraries. Every layer, from bencoding to uTP, follows the BEP specifications directly.

## Why

Most Go BitTorrent implementations wrap anacrolix/torrent or similar. stor exists to understand the protocol by implementing it, and to ship a self-contained daemon that replaces a Deluge + WebUI + browser extension stack with a single binary.

## Protocol Support

| BEP | Spec | Status |
|-----|------|--------|
| 3 | The BitTorrent Protocol | Implemented (choking, rarest-first, endgame) |
| 5 | DHT Protocol | Implemented (shared instance, configurable alpha) |
| 9 | Extension for Peers to Send Metadata Files | Implemented |
| 10 | Extension Protocol | Implemented |
| 11 | Peer Exchange (PEX) | Implemented |
| 15 | UDP Tracker Protocol | Implemented |
| 23 | Tracker Returns Compact Peer Lists | Implemented |
| 12 | Multitracker Metadata Extension | Implemented |
| 29 | uTP (Micro Transport Protocol) | Implemented (LEDBAT congestion control) |
| — | MSE/PE (Protocol Encryption) | Implemented (DH + RC4, plaintext fallback) |

## Features

- **Download**: rarest-first piece selection, endgame mode, resume with piece verification, multi-file support
- **Upload**: seeding after download, incoming peer listener, BEP 3 upload choking algorithm
- **Peers**: dynamic peer injection, periodic tracker re-announce, peer reconnection, PEX, multitracker
- **Transport**: TCP and uTP (LEDBAT), MSE/PE encryption with RC4, automatic plaintext fallback
- **Daemon**: JSON-RPC 2.0 API, persistent queue, configurable tuning, web-based settings
- **Web UI**: Deluge-inspired layout, real-time stats, filter/sort, settings editor
- **Chrome Extension**: intercepts `.torrent`/`magnet:` links, popup management
- **Docker**: multi-arch (amd64/arm64), ~9 MB distroless image

## Getting Started

```sh
make && ./stor daemon
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
peer/           Wire protocol, BEP 10/11 extensions, PEX
tracker/        HTTP and UDP announce clients
dht/            Kademlia routing table, iterative lookup, get_peers
magnet/         Magnet URI parsing, BEP 9 metadata fetch
torrent/        .torrent file parser
bencode/        Bencoding codec
mse/            Message Stream Encryption (DH + RC4)
utp/            Micro Transport Protocol (LEDBAT congestion control)
ui/             SvelteKit SPA, embedded in the binary at build time
extension/      Chrome extension, Manifest V3
```

## Building from Source

Requires Go 1.26+ and Bun.

```sh
make            # fmt → vet → lint → test → build
make test       # tests only
make build      # binary only (includes UI build)
```

## License

MIT
