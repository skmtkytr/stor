# stor

A BitTorrent client written from scratch in Go. No external torrent libraries — every layer from bencoding to DHT is implemented following the BEP specifications.

## Features

**Protocol**
- BEP 3 — BitTorrent core protocol with choking algorithm
- BEP 5 — DHT (Kademlia-based distributed hash table)
- BEP 9 — Magnet link metadata exchange (ut_metadata)
- BEP 10 — Extension protocol
- BEP 15 — UDP tracker
- BEP 23 — Compact peer lists

**Daemon**
- JSON-RPC 2.0 API with bearer token auth
- Persistent torrent queue with auto-resume
- Configurable concurrent downloads, peer limits, DHT tuning
- CORS support for browser extensions

**Web UI**
- Real-time torrent list with Deluge-inspired layout
- Toolbar with add/pause/resume/remove actions
- State filter tabs with counts
- Dynamic sorting (re-sorts on every poll)
- Progress bars with state labels and color coding
- Status bar: peers, DHT nodes, download speed, free disk space

**Chrome Extension**
- Intercepts `.torrent` and `magnet:` links on any page
- Right-click "Download with stor" context menu
- Popup with torrent list, sort, filter, and inline actions
- Configurable with connection test

## Quick Start

```sh
# Build everything (daemon + web UI)
make

# Start the daemon
./stor daemon

# Or one-shot download
./stor magnet:?xt=urn:btih:...
./stor path/to/file.torrent
```

On first run, the daemon creates `~/.config/stor/config.toml` with a generated API key:

```toml
port = 9090
download_dir = "/home/user/Downloads"
api_key = "sk-..."
state_path = "/home/user/.config/stor/state.json"
max_active = 5
```

Open `http://localhost:9090` to access the web UI.

## Configuration

All performance parameters are tunable via `config.toml` (zero or omitted = default):

| Key | Default | Description |
|-----|---------|-------------|
| `max_active` | 5 | Max concurrent downloading torrents |
| `max_peers` | 100 | Max peer connections per torrent |
| `max_pipeline` | 16 | Outstanding block requests per peer |
| `dial_timeout` | 3 | Peer connection timeout (seconds) |
| `numwant` | 200 | Peers requested from trackers |
| `dht_alpha` | 8 | DHT lookup parallelism |

## Chrome Extension

Load `extension/` as an unpacked extension in `chrome://extensions`. Configure the daemon URL and API key in the extension options page.

## Architecture

```
cmd/stor/       CLI entry point (daemon + one-shot modes)
daemon/         HTTP server, JSON-RPC dispatcher, config
engine/         Torrent lifecycle, queue management, session orchestration
download/       Piece-level download with peer manager and choking algorithm
peer/           Wire protocol: handshake, messages, extensions
tracker/        HTTP and UDP tracker announce
dht/            Kademlia DHT (routing table, iterative lookup, get_peers)
magnet/         Magnet URI parsing and metadata fetch
torrent/        .torrent file parsing
bencode/        Bencoding encoder/decoder
ui/             SvelteKit web UI (embedded in binary)
extension/      Chrome extension (Manifest V3)
```

## Building

Requirements: Go 1.26+, Bun (for web UI)

```sh
make          # lint + test + build
make test     # tests only
make build    # binary only
make ui       # web UI only
```

## License

MIT
