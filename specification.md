# loopguard 仕様書 (v2, 簡素化版)

llama-swap 経由で llama-server を起動する際に挟み込む、ループ検知・早期打ち切りプロキシ。
別セッションでの実装を想定した仕様書。

**設計方針: 薄いレイヤーであること。フラグは最小限。介入するのは「生成系エンドポイントの
ストリーム内容を見てループを検知し、検知したら打ち切る」ことだけ。それ以外は一切いじらず
素通しする。**

---

## 0. 背景

小型モデルが出力ループに陥ると、コンテキスト上限まで生成が続き数分〜十数分が無駄になる。
DRYサンプラー等での予防は小型モデルでは効果が不安定(無意味なトークンサラダ化)なため、
「ループを検知したら即座にクライアントへエラー相当を返し、呼び出し元の再試行を促す」
プロキシを作る。

上流への接続を切った際にllama-server側の生成が実際に止まるかどうかはバージョン・実装経路に
依存し不安定であることが分かっているが、これは今回のスコープ外とする。まずは「接続を切れば
止まる」という前提で実装し、実運用で問題が顕在化した場合に別途対処する。

---

## 1. 全体アーキテクチャ

```
llama-swap
   │ exec (cmd:)
   ▼
loopguard ── listen: ${PORT} (llama-swapが期待するポート)
   │ exec (子プロセスとして起動)
   ▼
llama-server ── listen: 動的に確保した空きポート(内部専用)
```

- llama-swapは「1つのコマンド」しか起動できないため、loopguardがllama-serverの代わりに
  llama-swapから直接execされる。
- loopguardは`${PORT}`で待ち受け、内部的に別ポートでllama-serverを起動し、リバースプロキシする。
- **生成系エンドポイント(2.2節)以外は完全に透過する。** ヘルスチェック、Web UI、静的アセット、
  `/metrics`、`/props`、`/slots`など、loopguardが把握していないパスも含め全てそのまま
  `httputil.ReverseProxy`で中継する(Goの`ReverseProxy`はWebSocketアップグレードも
  自動的に扱うため、Web UIが将来WebSocketを使い始めても特別な対応は不要)。
- 子プロセスがまだ起動していない間のリクエストは、単純に接続失敗による`502`が自動で返る
  (`ReverseProxy`のデフォルト挙動)。専用のヘルスチェック待ち受けロジックは実装しない。
  llama-swap自体が定期的にヘルスチェックをリトライするため、これで十分。

---

## 2. 起動引数の仕様

### 2.1 コマンドライン全体の形

```
loopguard [loopguard-flags...] -- <child-command> [child-args...]
```

`--` (ハイフン2つ、固定・変更不可)がloopguard自身のフラグと子プロセスのコマンドラインを
分離するセパレータ。`os.Args`を先頭から走査し、要素が正確に`"--"`と一致する最初の位置で
分割する。見つからない場合は起動時エラーで即終了。

`--help`フラグが指定された場合、使用方法をstderrへ出力して終了する。

### 2.2 loopguard 自身のフラグ(これで全部)

| フラグ | 型 | デフォルト | 説明 |
|---|---|---|---|
| `--port` | int | 必須 | loopguardが外部(llama-swap)向けに待ち受けるポート。`${PORT}`をそのまま渡す |
| `--child-port` | int | 0 (自動) | 子プロセスに割り当てる内部ポート。0なら自動確保 |
| `--loop-threshold-bytes` | int | 500 | ループとみなすために必要な「冗余バイト数」のしきい値。3章参照 |

以上3つのみ。ヘルスチェックパス、ログレベル、セパレータ文字列、finish_reason文言、子プロセス停止タイムアウト、最小周期、最大周期などは全てコード内定数として固定し、フラグ化しない。
必要になった時点で追加すればよい。

対象とする生成系エンドポイントは以下の4つに固定する(コード内定数、フラグ化しない):

- `POST /completion` (llama.cpp native)
- `POST /v1/completions`
- `POST /v1/chat/completions`
- `POST /infill`

上記以外は完全に素通し。

---

## 3. ループ検知しきい値の考え方

ループの1周期は短くとも50バイト、長ければ4000バイト程度になり得る想定。
本プロキシは文字列(バイト列)ベースであるため、周期の長さそのものではなく**「冗余バイト数」**で判定する。
これにより、短い周期のループでも十分に繰り返せば検知でき、長い周期のループでも少ない繰り返しで検知できる。

- `--loop-threshold-bytes` デフォルト 500 （周期 × (繰り返し回数 - 1) がこの値を超えた時点でループと判定）
- 最小周期 (`min-period-bytes`) デフォルト 1 ：コード内定数。1バイトのループでも検出対象。
- 最大周期 (`max-period-bytes`) デフォルト 4000 ：コード内定数。非常に長い周期を除外する保険。

例：
- 1バイトの周期（例：`！`の繰り返し）： $1 \times (\text{repeats} - 1) > 500 \Rightarrow$ **501回目**で切断
- 100バイトの周期： $100 \times (\text{repeats} - 1) > 500 \Rightarrow$ **6回目**で切断  
- 1000バイトの周期： $1000 \times (\text{repeats} - 1) > 500 \Rightarrow$ **2回目**で切断

この設定で最悪ケースの監視ウィンドウは `max-period-bytes + loop-threshold-bytes` = 4500バイト程度になる。
4章のアルゴリズムはこの程度のウィンドウなら余裕を持って高速に処理できる。

---

## 4. ループ検知アルゴリズム

### 4.1 対象
トークンIDではなく、**累積デルタテキスト(バイト列)** を対象にする。OpenAI互換エンドポイント
では生トークンIDが得られないため、native/OpenAI互換の両方に同一ロジックで対応できる
バイト列ベースを採用する。

### 4.2 手法: KMP接頭辞関数(prefix function / failure function)による最小周期検出

3章の通りウィンドウが大きくなったため、ナイープな全周期総当たり(周期候補ごとに窓全体を
比較する O(周期数 x 窓長) のアルゴリズム)は避け、標準的なKMP接頭辞関数を使って
「窓の最小周期」を1回の線形スキャンで求める。

```go
// window: 直近 W = maxPeriodBytes + loopThresholdBytes バイトのスライス(それより古い部分は破棄)
// 標準的なKMP prefix function
func prefixFunction(s []byte) []int {
    n := len(s)
    pi := make([]int, n)
    for i := 1; i < n; i++ {
        j := pi[i-1]
        for j > 0 && s[i] != s[j] {
            j = pi[j-1]
        }
        if s[i] == s[j] {
            j++
        }
        pi[i] = j
    }
    return pi
}

func detectLoop(window []byte, minPeriod, maxPeriod, thresholdBytes int) bool {
    n := len(window)
    if n == 0 {
        return false
    }
    pi := prefixFunction(window)
    period := n - pi[n-1] // このwindowの最小周期(定理: 常に成立する)
    if period < minPeriod || period > maxPeriod {
        return false
    }
    reps := n / period
    redundantBytes := period * (reps - 1)
    if redundantBytes <= thresholdBytes {
        return false
    }
    if isWhitespaceOnly(window[:period]) {
        return false // 空白/改行だけの周期は誤検知対策として無視(ハードコード、フラグ化しない)
    }
    return true
}
```

- リクエストごとに独立した状態(ウィンドウのバイトスライス)を持つこと。goroutine間で共有しない。
- 新しいSSEチャンクを受信するたびに、累積バッファへ追記し、末尾`W`バイトだけを残して
  古い部分は切り捨てたうえで`detectLoop`を呼ぶ。
- `W`バイト規模(3章の想定で最大5000バイト程度)であれば`prefixFunction`の計算コストは
  無視できるレベル(1回あたり最大でも数千回の比較)であり、生成が数千トークンに及んでも
  トータルで十分高速。

---

## 5. 生成系エンドポイント: 内部ストリーミング統一方針

**設計の核心(重要なので明記): クライアントが`stream: true`を指定したかどうかに
関わらず、loopguard から llama-server へのリクエストは常に`stream: true`を強制する。**
SSEレスポンスのヘッダーには`X-Accel-Buffering: no`と`Cache-Control: no-cache`を設定し、
中継HTTPレイヤーのバッファリングを防ぐ。**

理由: 非ストリーミングのレスポンスはllama-server内部で生成完了まで確定しないため、外部から
早期打ち切りを判定する材料(部分テキスト)が得られない。常に内部だけSSEにすることで、
非SSEクライアント向けにも同じ検知ロジックを適用できる。これを行わない限りstream:falseの
リクエストは原理的に「早く失敗させる」ことができないため、これは妥協できない要件。

処理フロー:

1. クライアントのリクエストボディをJSONとしてパースし、元の`stream`値を`clientWantsStream`
   として保持する(未指定はfalse扱い)。
2. リクエストボディの`stream`を`true`に書き換えて子プロセスへPOSTする。
3. `text/event-stream`としてレスポンスを受え取り、`data: ...`行を1行ずつパースしながら:
   - デルタテキストを取り出し、ウィンドウバッファに追記する。
   - `detectLoop`を呼ぶ。
   - `clientWantsStream == true`の場合: 受信したチャンクを**即座に(`http.Flusher.Flush()`)**クライアントへ中継する。
   - `clientWantsStream == false`の場合: クライアントへは何も送らず、内部で蓄積するだけ。
4. ループ検知がトリガーされた場合:
   - 子プロセスへのHTTPリクエストのcontextをcancelし、コネクションを閉じる。
   - クライアントへは6章の形式で打ち切りを通知する。
5. 正常終了(ループ未検知でストリーム終端に到達)の場合は、通常のOpenAI互換の終端処理
   (`data: [DONE]`の中継、または非ストリーム時は蓄積結果から完全なレスポンスJSONを
   組み立てる)を行う。

エンドポイント別のデルタ位置・終端条件のマッピング(実装時に実際のllama-serverバージョンの
レスポンスで最終確認すること):

| エンドポイント | デルタ位置 | 終端条件 |
|---|---|---|
| `/completion` (native) | `content` | 最終オブジェクトで`"stop": true` |
| `/v1/completions` | `choices[0].text` | `data: [DONE]`、または`finish_reason`が非null |
| `/v1/chat/completions` | `choices[0].delta.content` | `data: [DONE]`、または`finish_reason`が非null |
| `/infill` | `content`(nativeと同様想定) | `"stop": true` |

---

## 6. 検知後の応答

- finish_reasonの値は`"length"`固定(コード内定数、フラグ化しない)。OpenAI互換SDKの多くが
  未知の値より標準値の方が安全に扱えるため。
- HTTPステータスコードは`200`を維持する(既存クライアントの異常系分岐を誤爆させないため)。
- **ストリーミングクライアント向け**: 検知時点までに中継したチャンクを維持したまま、
  `finish_reason: "length"`とした最終チャンクを送り、続けて`data: [DONE]`を送って接続を閉じる。
- **非ストリーミングクライアント向け**: 検知時点までに蓄積したテキストを使って、そのエンドポイント
  の非ストリーミングレスポンス形状を模したJSONを組み立て、`finish_reason: "length"`にして返す。

打ち切りだったかどうかを機械的に判別したいクライアント向けの追加フィールドは今回のv1では
実装しない(finish_reason: "length"のみで十分。必要になれば後で足す)。

---

## 7. ポート解決

### 7.1 内部ポートの確保
`--child-port`が0(デフォルト)の場合、`net.Listen("tcp", "127.0.0.1:0")`で空きポートを取得し、
ポート番号を読んでからいったんリスナーをCloseする(ごく小さな競合ウィンドウは既知の限界として
コード上にコメントを残す)。

### 7.2 子プロセスへのポート指定方法(環境変数優先)
llama-serverは`--port`に対応する環境変数`LLAMA_ARG_PORT`をサポートしている。ただし
**CLI引数は環境変数より優先される**ため、子プロセスのargv(`--`以降)に`--port`系トークンが
含まれていると環境変数が無視されてしまう。そこで:

1. 子プロセスのargvを走査し、`--port <値>`(空白区切り)および`--port=<値>`(等号区切り)の
   両方の形を**トークンごと除去する**(値を書き換えるのではなく、丸ごと取り除く)。
2. 子プロセスを起動する際、環境変数`LLAMA_ARG_PORT=<7.1で確保した内部ポート>`を設定する。

この方式により、YAML側で`--port ${PORT}`をllama-server用の引数として書いていても書いていなくても、
loopguardが常に正しい内部ポートを子に伝えられる。argvの値をパースして書き換える必要がなく、
除去するだけなので実装がシンプルかつ`--port=8080`のような別記法にも同じロジックで対応できる。

---

## 8. プロセス管理

- 子プロセス起動は`exec.Command`。標準出力・標準エラーはそのままloopguardの標準出力・標準エラーへ
  パイプする(llama-swapのログ収集がloopguardのstdout/stderrを見ているため)。
- `SIGTERM`/`SIGINT`を受け取ったら子プロセスへ転送し、固定5秒待って終了しなければ`SIGKILL`する
  (この5秒はコード内定数、フラグ化しない)。その後loopguard自身も終了する。
- 子プロセスが予期せず終了した場合、loopguardも同じexit codeで終了する。

---

## 9. 設定例(llama-swap側)

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
  gemma-4-e4b: # unsloth Gemma 4 E4B Q4_K_M
    cmd: |
      ${latest-llama}
      --model /app/models/gemma-4-E4B-it-Q4_K_M.gguf
      --mmproj /app/models/gemma-4-E4B-mmproj.gguf
      --ctx-size 200000
      --n-gpu-layers 99
      -ctk q8_0
      -ctv q8_0
      --flash-attn on
      --spec-type ngram-map-k4v
    ttl: 600
```

`--model`以降のモデル固有フラグはmacro展開後の文字列末尾に連結されるため、自然に`--`の
右側(= llama-serverへの引数)に含まれる。ポート算術は一切不要。

---

## 10. Dockerfile統合

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
# ...既存の内容はそのまま...
COPY --from=loopguard-builder /out/loopguard /usr/local/bin/loopguard
```

- `CGO_ENABLED=0`で静的バイナリにする(ランタイムイメージが何であっても動くようにするため)。
- `GOARCH`はビルド環境に合わせて調整する。
- llama-swap側の設定変更は9章の通りYAMLのみで完結し、Dockerfile以外の変更は不要。

---

## 11. テスト観点・受け入れ基準

1. **正常系**: `/v1/chat/completions`・`/completion`(stream true/false両方)で、通常の応答が
   loopguardを介さない場合と一致すること。
2. **異常系**: 意図的にループを誘発し、`loop-threshold-bytes`相当（例: 500バイトの冗材量）が生成された時点で
   クライアントへの応答が終了すること。stream true/falseどちらでも`finish_reason: "length"`で
   終わること。
3. **並行性**: `--parallel`を2以上にしたllama-serverに対し複数リクエストを同時に投げ、
   一方がループ検知で打ち切られても他方が正常に完了すること(検知状態がリクエスト間で
   漏れていないこと)。
4. **プロセス管理**: `docker stop`でloopguard・llama-server双方が正しく終了すること。
   llama-serverのログがllama-swapのログ画面から従来通り見えること。
5. **ポート**: `--child-port`未指定時、複数モデルを同時起動(matrix構成)しても内部ポートが
   衝突しないこと。子プロセスのargvに`--port`があってもなくても正しい内部ポートで起動すること。
6. **透過性**: `/`, `/props`, `/slots`, `/metrics`など生成系以外の全パスが素の`llama-server`を
   直接叩いた場合と同じ応答になること(Web UIも含め動作すること)。

---

## 12. 既知の制限・将来拡張

- 上流llama-serverの接続切断時の挙動はバージョン依存で不安定(0章参照)。接続を切っても
  実際にはスロットの計算が止まらないケースがあり得る。問題が顕在化したら別途対処する。
- 文字列ベースの検知のため、意味的な言い換えを伴うループ(同じ内容を毎回微妙に違う表現で
  繰り返すケース)は検知できない。
- v1では対象4エンドポイント固定・フラグでの拡張不可。新エンドポイントへの対応が必要になったら
  コードの定数リストに追加する形で対応する。