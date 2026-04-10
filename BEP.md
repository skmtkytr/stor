# BEP (BitTorrent Enhancement Proposal) 実装対応表

ref: https://www.bittorrent.org/beps/bep_0000.html

## 実装ロードマップ

### Phase 1: コアプロトコル

| BEP | タイトル | 状態 | 対応パッケージ | 実装状況 |
|-----|---------|------|--------------|---------|
| [BEP 3](https://www.bittorrent.org/beps/bep_0003.html) | The BitTorrent Protocol Specification | Final | `bencode/`, `torrent/`, `tracker/`, `peer/`, `download/` | ✅ 完了 |
| [BEP 23](https://www.bittorrent.org/beps/bep_0023.html) | Tracker Returns Compact Peer Lists | Accepted | `tracker/` | ✅ 完了 |

**BEP 3 が定義する範囲（全ての基礎）:**

| 要素 | 説明 | パッケージ | 実装状況 |
|------|------|-----------|---------|
| Bencoding | シリアライゼーション形式 (string, int, list, dict) | `bencode/` | ✅ 完了 |
| Metainfo (.torrent) | announce URL, info dict, pieces, files | `torrent/` | ✅ 完了 |
| HTTP Tracker | announce リクエスト/レスポンス (peer リスト取得) | `tracker/` | ✅ 完了 |
| Peer Handshake | `\x13BitTorrent protocol` + reserved + info_hash + peer_id (68 bytes) | `peer/` | ✅ 完了 |
| Peer Messages | choke/unchoke/interested/have/bitfield/request/piece/cancel | `peer/` | ✅ 完了 |
| Piece Download | ブロック単位 (16KiB) のリクエスト → 検証 (SHA1) → ファイル書き出し | `download/` | ✅ 完了 |

**BEP 23 (Compact Peer Lists):**
- Tracker レスポンスの `peers` が 6バイト/peer のバイナリ文字列 (4byte IP + 2byte port)
- ほぼ全てのトラッカーがこの形式を返すため、実質必須

### Phase 2: 拡張プロトコル + マグネットリンク

| BEP | タイトル | 状態 | 対応パッケージ | 実装状況 |
|-----|---------|------|--------------|---------|
| [BEP 10](https://www.bittorrent.org/beps/bep_0010.html) | Extension Protocol | Accepted | `peer/` | ✅ 完了 |
| [BEP 9](https://www.bittorrent.org/beps/bep_0009.html) | Extension for Peers to Send Metadata Files | Accepted | `magnet/`, `peer/` | ✅ 完了 |
| [BEP 15](https://www.bittorrent.org/beps/bep_0015.html) | UDP Tracker Protocol | Accepted | `tracker/` | ✅ 完了 |

**BEP 10 (Extension Protocol):**
- Peer wire protocol の msg ID 20 を使い、拡張メッセージを多重化
- Handshake で `m` dict を交換し、拡張ごとの msg ID をネゴシエーション
- BEP 9 (メタデータ転送) 等の基盤

**BEP 9 (Metadata Transfer = マグネットリンク対応):**
- `magnet:?xt=urn:btih:<info_hash>` から info dict を peer 経由で取得
- BEP 10 の `ut_metadata` 拡張として動作
- メタデータを 16KiB ブロック単位で転送

**BEP 15 (UDP Tracker):**
- HTTP より軽量 (4パケット ~618B vs 10パケット ~1206B)
- connect → announce の 2段階。connection_id で IP spoofing 防止
- 多くのトラッカーが UDP を採用しているため必要

### Phase 3: DHT (トラッカーレス)

| BEP | タイトル | 状態 | 対応パッケージ | 実装状況 |
|-----|---------|------|--------------|---------|
| [BEP 5](https://www.bittorrent.org/beps/bep_0005.html) | DHT Protocol | Accepted | `dht/` | ✅ 完了 |

**BEP 5 (DHT):**
- Kademlia ベースの分散ハッシュテーブル
- UDP 上の KRPC プロトコル (bencode dict)
- 4つのクエリ: `ping`, `find_node`, `get_peers`, `announce_peer`
- XOR 距離メトリック、K-bucket ルーティングテーブル (K=8)

### 将来的に対応を検討

| BEP | タイトル | 状態 | 用途 |
|-----|---------|------|------|
| [BEP 6](https://www.bittorrent.org/beps/bep_0006.html) | Fast Extension | Accepted | have-all/have-none/reject/suggest/allowed-fast |
| [BEP 11](https://www.bittorrent.org/beps/bep_0011.html) | Peer Exchange (PEX) | Accepted | Peer 同士で peer リストを交換 |
| [BEP 12](https://www.bittorrent.org/beps/bep_0012.html) | Multitracker Metadata Extension | Accepted | announce-list (複数 tracker) |
| [BEP 19](https://www.bittorrent.org/beps/bep_0019.html) | WebSeed (GetRight) | Accepted | HTTP/FTP サーバからピース取得 |
| [BEP 27](https://www.bittorrent.org/beps/bep_0027.html) | Private Torrents | Accepted | DHT/PEX 無効化フラグ |
| [BEP 29](https://www.bittorrent.org/beps/bep_0029.html) | uTP (Micro Transport) | Accepted | UDP ベースの輻輳制御付きトランスポート |
| [BEP 52](https://www.bittorrent.org/beps/bep_0052.html) | BitTorrent v2 | Draft | Merkle tree, SHA-256, per-file piece tree |

## プロトコル主要フォーマット早見表

### Peer Handshake (68 bytes)
```
[1 byte]  pstrlen = 19
[19 bytes] pstr    = "BitTorrent protocol"
[8 bytes]  reserved (bit 20 from right = BEP 10 support)
[20 bytes] info_hash
[20 bytes] peer_id
```

### Peer Messages
```
[4 bytes] length prefix (big-endian)
[1 byte]  message ID
[payload]

ID | Name         | Payload
---|-------------|--------
 0 | choke        | (なし)
 1 | unchoke      | (なし)
 2 | interested   | (なし)
 3 | not interested | (なし)
 4 | have         | [4] piece index
 5 | bitfield     | [N] bit per piece
 6 | request      | [4] index + [4] begin + [4] length
 7 | piece        | [4] index + [4] begin + [N] block
 8 | cancel       | [4] index + [4] begin + [4] length
20 | extended     | [1] ext msg ID + [N] payload (BEP 10)
```

### HTTP Tracker Announce
```
GET /announce?
  info_hash=<20 bytes url-encoded>
  &peer_id=<20 bytes>
  &port=<listen port>
  &uploaded=<bytes>
  &downloaded=<bytes>
  &left=<bytes remaining>
  &compact=1
  &event=<started|completed|stopped>
```

### UDP Tracker (BEP 15)
```
Connect:  [8] magic 0x41727101980 + [4] action=0 + [4] txn_id
          → [4] action=0 + [4] txn_id + [8] conn_id

Announce: [8] conn_id + [4] action=1 + [4] txn_id + [20] info_hash
          + [20] peer_id + [8] downloaded + [8] left + [8] uploaded
          + [4] event + [4] IP + [4] key + [4] num_want + [2] port
          → [4] action=1 + [4] txn_id + [4] interval + [4] leechers
            + [4] seeders + [6*N] peers (IP+port)
```
