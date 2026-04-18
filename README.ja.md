# stor

[English](README.md)

**外部ライブラリゼロ、単一バイナリの BitTorrent クライアント。** デーモン・Web UI・14 の BEP を bencode から uTP までフルスクラッチで Go 実装。

## 動機

Go の BitTorrent 実装の多くは anacrolix/torrent 等のラッパー。stor は Deluge + WebUI + ブラウザ拡張を `go build` 一発、9 MB の Docker イメージに置き換える。

## プロトコル対応状況

| BEP | 仕様 | 状態 |
|-----|------|------|
| 3 | BitTorrent プロトコル | 実装済（チョーキング、rarest-first、endgame） |
| 5 | DHT プロトコル | 実装済（共有インスタンス、alpha 設定可） |
| 6 | Fast Extension | 実装済（have-all、have-none、reject） |
| 7 | IPv6 Tracker Extension | 実装済（peers6、ipv6= announce パラメータ） |
| 9 | メタデータ交換拡張 | 実装済 |
| 10 | 拡張プロトコル | 実装済 |
| 11 | ピア交換 (PEX) | 実装済（IPv4 + IPv6 added6/dropped6 対応） |
| 12 | マルチトラッカー拡張 | 実装済 |
| 15 | UDP トラッカープロトコル | 実装済 |
| 19 | WebSeed (GetRight) | 実装済（HTTP Range、マルチファイル対応） |
| 23 | コンパクトピアリスト | 実装済 |
| 27 | Private Torrents | 実装済（DHT/PEX 自動無効化） |
| 29 | uTP (Micro Transport Protocol) | 実装済（LEDBAT 輻輳制御） |
| 52 | BitTorrent v2 | 部分対応（hybrid パース + v1 ダウンロード） |
| — | MSE/PE（プロトコル暗号化） | 実装済（768-bit DH + RC4、平文フォールバック） |

詳細は [BEP.md](BEP.md) を参照。

## 主な機能

- **ダウンロード**: rarest-first ピース選択、endgame モード、ピース検証付きレジューム、マルチファイル対応
- **アップロード**: ダウンロード完了後の自動シード、incoming ピア受付、BEP 3 チョーキングアルゴリズム
- **ピア管理**: 動的ピア注入、定期 re-announce、ピア再接続、PEX、マルチトラッカー
- **トランスポート**: TCP / uTP (LEDBAT)、MSE/PE 暗号化（RC4）、自動平文フォールバック
- **デーモン**: JSON-RPC 2.0 API、永続キュー、チューニング設定、Web 設定画面
- **Web UI**: Deluge 風レイアウト、リアルタイム統計、フィルタ/ソート、設定エディタ
- **Chrome 拡張**: `.torrent` / `magnet:` リンク自動キャプチャ、ポップアップ管理
- **Docker**: マルチアーキ（amd64/arm64）、約 9 MB distroless イメージ

## はじめる

```sh
make && ./stor daemon
```

初回起動時に `~/.config/stor/config.toml` が生成され API キーが自動発行される。Web UI は `http://localhost:9090` で提供。

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

### スタンドアロンダウンロード

```sh
./stor magnet:?xt=urn:btih:...
./stor path/to/file.torrent [出力先]
```

## 設定

`config.toml` の全パラメータは省略可。表示値はデフォルト。

```toml
port = 9090
download_dir = "~/Downloads"
tmp_dir = ""          # DL中の一時フォルダ（空 = download_dir を使用）
max_active = 5
log_level = "info"    # debug, info, warn, error

# ピア・トラッカーのチューニング
max_peers = 100       # トレントあたりの同時接続数
max_pipeline = 16     # ピアあたりの未応答リクエスト数
dial_timeout = 3      # 秒
numwant = 200         # トラッカーへのピア要求数
dht_alpha = 8         # DHT lookup の並列度
enable_utp = false    # uTP トランスポート有効化（LEDBAT 輻輳制御）
```

ほとんどの設定は Web UI の設定画面または `daemon.setConfig` RPC から実行時に変更可能。

## API

デーモンは `POST /api/rpc` で JSON-RPC 2.0 エンドポイントを公開。認証は `Authorization: Bearer <api_key>` ヘッダー。

| メソッド | 説明 |
|---------|------|
| `torrent.add` | magnet URI または .torrent ファイルパスでトレント追加 |
| `torrent.addFile` | Base64 エンコードされた .torrent データで追加 |
| `torrent.addURL` | HTTP URL でトレント追加（SSRF 対策済） |
| `torrent.remove` | ID でトレント削除 |
| `torrent.pause` | トレント一時停止 |
| `torrent.resume` | 一時停止したトレントを再開 |
| `torrent.get` | ID でトレント詳細取得 |
| `torrent.list` | 全トレントの一覧と統計 |
| `torrent.peers` | トレントの接続中ピア一覧 (アドレス、転送、クライアント、速度) |
| `torrent.queueTop` | キュー先頭に移動 |
| `torrent.queueUp` | キュー位置を1つ上へ |
| `torrent.queueDown` | キュー位置を1つ下へ |
| `torrent.queueBottom` | キュー末尾に移動 |
| `daemon.stats` | グローバル統計（速度、ピア数、DHT サイズ） |
| `daemon.setMaxActive` | 最大同時ダウンロード数の変更 |
| `daemon.setConfig` | 実行時設定の更新 |
| `daemon.version` | バージョン文字列を返す |

追加の REST エンドポイント:

| エンドポイント | 説明 |
|--------------|------|
| `POST /api/add` | フォームベースの追加（Chrome 拡張用） |
| `GET /api/torrents` | トレント一覧（ポーリング用） |
| `GET /` | Web UI |

## セキュリティ

stor は悪意あるピア・トラッカー・トレントに対する多層防御を実装:

| レイヤー | 保護内容 |
|---------|---------|
| **入力パース** | bencode 制限: 文字列 64MB、コレクション 100万件、累積 200万件、深さ 100 |
| **メタデータ** | メタデータサイズ上限 32MB、SHA1 info hash 検証 |
| **トラッカー** | レスポンス 10MB 制限、ピア数 1万件上限、DNS ピニング（リバインド防止） |
| **ピア** | bitfield サイズ検証、ブロック境界チェック、ピースハッシュ検証 |
| **アップロード** | ブロック 32KiB 上限、ピース index 検証、オフセット境界チェック |
| **ネットワーク** | トラッカー/DHT 結果のプライベート IP フィルタリング（ループバック/RFC1918/リンクローカル） |
| **DHT** | ノード 1K / ピア 5K のルックアップ制限、トークンベース announce、pending TTL |
| **トランスポート** | uTP 接続制限 (4096)、incoming ピア制限 (200)、ダイヤルセマフォ (50) |
| **ファイルシステム** | パストラバーサル防止、シンボリックリンク解決、トレント名サニタイズ |
| **デーモン** | API キー認証、リクエストサイズ制限、PID ファイル管理、CORS オリジン反映 |
| **暗号化** | 768-bit DH 鍵交換、RC4 + 1024 バイトキーストリーム破棄 |

## Chrome 拡張

`chrome://extensions` で `extension/` を読み込む。

- `.torrent` / `magnet:` リンクの自動キャプチャ
- 右クリックメニュー
- ポップアップからのトレント管理
- 接続テスト付きの設定画面

## プロジェクト構成

```
cmd/stor/       エントリポイント — デーモン / スタンドアロン
daemon/         HTTP サーバー、JSON-RPC 2.0 API、設定管理
engine/         トレントライフサイクル、キュースケジューリング、マグネット並列解決
download/       ピース I/O、rarest-first、endgame、ピアマネージャー、チョーキング
storage/        マルチファイル書き込み、ピース検証、ファイルハンドルキャッシュ
peer/           ワイヤープロトコル、BEP 6/10/11 拡張、PEX
tracker/        HTTP / UDP アナウンスクライアント（DNS ピニング対応）
dht/            Kademlia ルーティングテーブル、反復探索、get_peers
magnet/         マグネット URI パース、BEP 9 メタデータ取得
torrent/        .torrent ファイルパーサー（v1 + hybrid v2）
bencode/        bencode コーデック（DoS 耐性強化済）
mse/            Message Stream Encryption（768-bit DH + RC4）
utp/            Micro Transport Protocol（LEDBAT 輻輳制御）
ui/             SvelteKit SPA（ビルド時にバイナリへ埋め込み）
extension/      Chrome 拡張（Manifest V3）
```

## ソースからのビルド

Go 1.26+ と Bun が必要。

```sh
make            # fmt → vet → lint → test → build
make test       # テストのみ
make build      # バイナリのみ（UI ビルド含む）
```

## ライセンス

MIT
