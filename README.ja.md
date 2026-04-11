# stor

[English](README.md)

Go でフルスクラッチ実装した BitTorrent クライアント。外部のトレントライブラリに依存せず、bencode から uTP まで BEP 仕様に準拠して構築。

## 動機

Go の BitTorrent 実装の多くは anacrolix/torrent 等のラッパー。stor はプロトコルを理解するために一から実装し、Deluge + WebUI + ブラウザ拡張という構成を単一バイナリで置き換えることを目指している。

## プロトコル対応状況

| BEP | 仕様 | 状態 |
|-----|------|------|
| 3 | BitTorrent プロトコル | 実装済（チョーキング、rarest-first、endgame） |
| 5 | DHT プロトコル | 実装済（共有インスタンス、alpha 設定可） |
| 9 | メタデータ交換拡張 | 実装済 |
| 10 | 拡張プロトコル | 実装済 |
| 11 | ピア交換 (PEX) | 実装済 |
| 15 | UDP トラッカープロトコル | 実装済 |
| 23 | コンパクトピアリスト | 実装済 |
| 6 | Fast Extension | 実装済（have-all、have-none、reject） |
| 12 | マルチトラッカー拡張 | 実装済 |
| 19 | WebSeed (GetRight) | 実装済（HTTP Range、マルチファイル対応） |
| 27 | Private Torrents | 実装済（DHT/PEX 自動無効化） |
| 29 | uTP (Micro Transport Protocol) | 実装済（LEDBAT 輻輳制御） |
| — | MSE/PE（プロトコル暗号化） | 実装済（DH + RC4、平文フォールバック） |

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
peer/           ワイヤープロトコル、BEP 10/11 拡張、PEX
tracker/        HTTP / UDP アナウンスクライアント
dht/            Kademlia ルーティングテーブル、反復探索、get_peers
magnet/         マグネット URI パース、BEP 9 メタデータ取得
torrent/        .torrent ファイルパーサー
bencode/        bencode コーデック
mse/            Message Stream Encryption（DH + RC4）
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
