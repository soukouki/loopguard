# loopguard

`loopguard` は、`llama-swap` と `llama-server` の間に配置する軽量なストリーミングプロキシであり、LLMの出力ループ（同じ文字列の繰り返し）を検知して、ストリームを早期に打ち切るためのツールです。

## 概要

小型モデルなどで発生する出力ループは、コンテキスト上限まで生成が続き、時間とリソースを無駄に消費します。`loopguard` は、生成中のテキストをリアルタイムに監視し、一定の繰り返しパターンが検出された時点で接続を遮断し、クライアントに早期終了を通知することでこの問題を解決します。

## アーキテクチャ

`loopguard` は `llama-swap` と `llama-server` の間に位置し、透過的なプロキシとして動作します。

```
llama-swap
   │ exec (cmd:)
   ▼
loopguard ── listen: ${PORT} (llama-swapが期待するポート)
   │ exec (子プロセスとして起動)
   ▼
llama-server ── listen: 動的に確保した内部ポート
```

- `llama-swap` は1つのコマンドしか起動できないため、`loopguard` が `llama-server` の代わりに起動されます。
- `loopguard` は指定されたポートで待ち受け、内部的に `llama-server` を別ポートで起動してリバースプロキシします。
- 生成系エンドポイント以外のリクエスト（ヘルスチェック、`/metrics`、Web UIなど）は、すべてそのまま透過的に中継されます。

## 導入方法 (llama-swap YAML設定)

`llama-swap` の設定ファイルでマクロを使用して定義します。

```yaml
macros:
  latest-llama: >
    loopguard
    --port ${PORT}
    --loop-threshold-bytes 500
    --
    llama-server
    --port ${PORT}
    --metrics

models:
  gemma-4-e4b:
    cmd: |
      ${latest-llama}
      --model /app/models/gemma-4-E4B-it-Q4_K_M.gguf
      --ctx-size 200000
      --n-gpu-layers 99
```

## 機能と仕様

### インターセプトするエンドポイント
以下の4つの生成エンドポイントのみを監視し、ループ検知を行います。
- `POST /completion`
- `POST /v1/completions`
- `POST /v1/chat/completions`
- `POST /infill`

### 主要フラグ
| フラグ | 説明 | デフォルト |
|---|---|---|
| `--port` | `loopguard` が外部（llama-swap）向けに待ち受けるポート（必須） | - |
| `--child-port` | 内部子プロセスのポート（0で自動確保） | `0` |
| `--loop-threshold-bytes` | カットオフ前の冗長バイト数しきい値 | `500` |

※ `min-period` (デフォルト 1) および `max-period` (デフォルト 4000) はハードコードされた定数であり、フラグとして指定することはできません。

### ループ検知の仕組み
累積デルタバイトストリームに対してKMP接頭辞関数（失敗関数）を用いて出力ループを検知します。`period × (repeats − 1)` が `--loop-threshold-bytes` を超えた場合にループと判定されます。これにより、短周期のループ（1バイト）はフラグが立つまでより多くの繰り返しが必要になりますが、長周期のループはわずか2〜3回の繰り返しで検知されます。例えば、1バイトの周期は501バイトでカットされ、100バイトの周期は6回の繰り返しで、1000バイトの周期はわずか2回の繰り返しで検知されます。ループ検知時、上流への接続をキャンセルし、`finish_reason: "length"` として応答を終了させます。

## Dockerfile 統合例

`loopguard` を静的バイナリとしてビルドし、ランタイムイメージにコピーして使用します。

```dockerfile
# --- loopguard builder ---
FROM golang:1.23-alpine AS loopguard-builder
WORKDIR /src
COPY loopguard/go.mod loopguard/go.sum ./
RUN go mod download
COPY loopguard/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/loopguard ./cmd/loopguard

# --- 既存のランタイムステージ ---
FROM <既存のベースイメージ>
COPY --from=loopguard-builder /out/loopguard /usr/local/bin/loopguard
```
