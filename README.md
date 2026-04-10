# stor

A BitTorrent client written from scratch in Go. No external torrent libraries — every layer from bencoding to DHT is implemented following the BEP specifications.

## Features

**Protocol**
- BEP 3 — Core protocol with choking algorithm
- BEP 5 — Kademlia DHT
- BEP 9/10 — Magnet links via extension protocol
- BEP 15 — UDP tracker
- BEP 23 — Compact peer lists

**Architecture**: Daemon + Web UI + Chrome Extension — like Deluge, but a single static binary.

## Quick Start

```sh
make && ./stor daemon
```

First run generates `~/.config/stor/config.toml` with an API key. Open `http://localhost:9090`.

### Docker

```yaml
services:
  stor:
    image: ghcr.io/skmtkytr/stor:latest
    ports:
      - "9090:9090"
      - "6881:6881"
      - "6881:6881/udp"
    volumes:
      - ./config:/config
      - ./data:/data
    restart: unless-stopped
```

### One-shot

```sh
./stor magnet:?xt=urn:btih:...
./stor path/to/file.torrent [output-dir]
```

## Configuration

`config.toml` — all values optional (shown = defaults):

```toml
port = 9090
download_dir = "~/Downloads"
max_active = 5

# Performance tuning
max_peers = 100
max_pipeline = 16
dial_timeout = 3
numwant = 200
dht_alpha = 8
```

## Chrome Extension

Load `extension/` as unpacked in `chrome://extensions`. Intercepts `.torrent` and `magnet:` links, right-click menu, popup with torrent management.

## Architecture

```
cmd/stor/       CLI (daemon + one-shot)
daemon/         HTTP server, JSON-RPC 2.0, config
engine/         Session lifecycle, queue, parallel magnet resolution
download/       Piece download, peer manager, BEP 3 choking
peer/           Wire protocol, extensions (BEP 10)
tracker/        HTTP + UDP announce
dht/            Kademlia routing, iterative lookup, get_peers
magnet/         URI parsing, metadata fetch (BEP 9)
torrent/        .torrent parsing
bencode/        Encoder/decoder
ui/             SvelteKit web UI (embedded in binary)
extension/      Chrome extension (Manifest V3)
```

## Building

Go 1.26+, Bun

```sh
make          # lint + test + build
make build    # binary only
make ui       # web UI only
```

## License

MIT
