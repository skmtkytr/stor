# stor

[English](README.md)

Go でフルスクラッチ実装した BitTorrent クライアント。外部のトレントライブラリに依存せず、bencode から Kademlia DHT まで BEP 仕様に準拠して構築。

## 動機

Go の BitTorrent 実装の多くは anacrolix/torrent 等のラッパー。stor はプロトコルを理解するために一から実装し、Deluge + WebUI + ブラウザ拡張という構成を単一バイナリで置き換えることを目指している。

## プロトコル対応状況

| BEP | 仕様 | 状態 |
|-----|------|------|
| 3 | BitTorrent プロトコル | 実装済（チョーキングアルゴリズム含む） |
| 5 | DHT プロトコル | 実装済 |
| 9 | メタデータ交換拡張 | 実装済 |
| 10 | 拡張プロトコル | 実装済 |
| 15 | UDP トラッカープロトコル | 実装済 |
| 23 | コンパクトピアリスト | 実装済 |

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
    ports:
      - "9090:9090"
      - "6881:6881"
      - "6881:6881/udp"
    volumes:
      - ./config:/config
      - ./data:/data
    restart: unless-stopped
```

マルチアーキテクチャ対応（amd64 / arm64）。イメージサイズ約 9 MB（distroless）。

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
max_active = 5

# ピア・トラッカーのチューニング
max_peers = 100       # トレントあたりの同時接続数
max_pipeline = 16     # ピアあたりの未応答リクエスト数
dial_timeout = 3      # 秒
numwant = 200         # トラッカーへのピア要求数
dht_alpha = 8         # DHT lookup の並列度
```

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
download/       ピース I/O、ピアマネージャー、BEP 3 チョーキング
peer/           ワイヤープロトコル、BEP 10 拡張ネゴシエーション
tracker/        HTTP / UDP アナウンスクライアント
dht/            Kademlia ルーティングテーブル、反復探索、get_peers
magnet/         マグネット URI パース、BEP 9 メタデータ取得
torrent/        .torrent ファイルパーサー
bencode/        bencode コーデック
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
