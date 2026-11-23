# recfriio（Go 版）

Linux 向けの USB デジタルテレビチューナー録画プログラム recfriio のフォークです。  
2026-11-19 に、C/C++ の recfriio を Go で書き直したものに置き換えました。  
cgo・libusb・libpcsclite を使わず、Linux の usbfs と pcscd に直接話す 1 つのバイナリです。

| 機器 | USB ID | 受けられるもの | B25 のカード | 詳しい説明 |
|---|---|---|---|---|
| Quixun QRS-UT100B | `3275:7020`（ファーム無しは `1738:5211`） | 地デジ・CATV | 内蔵のスロット | [docs/QRS-UT100B.md](docs/QRS-UT100B.md) |
| SKNET MonsterTV U3S | `3275:9015` | 地デジ・BS・CS | 内蔵のスロット | [docs/SK-MTVU3S.md](docs/SK-MTVU3S.md) |

C/C++ 版（Friio・HDUS・HDP などの古い機種を含む）は、タグ [`cpp-final`](../../tree/cpp-final) に残しています。

## 作り方

Go 1.24 以上が必要です。

```bash
CGO_ENABLED=0 go build -o bin/ ./cmd/...
```

手元に Go を入れない場合は、Docker の Go で作れます。

```bash
scripts/go.sh build -o bin/ ./cmd/...
```

`bin/` に次の 4 つができます。

| コマンド | 中身 |
|---|---|
| `recfriio` | 録画のコマンド。機種は引数・機器・チャンネルから決める |
| `recut100b` | `recfriio --hdp` と同じ（前からある名前） |
| `recmtvu3s` | `recfriio --u3s` と同じ（前からある名前） |
| `ut100b-firmware` | UT100B のファームの取り出しと送り込み |

## 準備

udev で、使う機器の USB ID に書き込み権限を与えます。

```text
# /etc/udev/rules.d/60-recfriio.rules
SUBSYSTEM=="usb", ENV{DEVTYPE}=="usb_device", ATTR{idVendor}=="1738", ATTR{idProduct}=="5211", MODE="0660", GROUP="plugdev"
SUBSYSTEM=="usb", ENV{DEVTYPE}=="usb_device", ATTR{idVendor}=="3275", ATTR{idProduct}=="7020", MODE="0660", GROUP="plugdev"
SUBSYSTEM=="usb", ENV{DEVTYPE}=="usb_device", ATTR{idVendor}=="3275", ATTR{idProduct}=="9015", MODE="0660", GROUP="plugdev"
```

UT100B は、電源が入るたびにファームを送る必要があります。  
ファームの取り出し方は [docs/QRS-UT100B.md](docs/QRS-UT100B.md#go-版での使い方) にあります。

## 使い方

引数の形は C/C++ 版の recfriio・recpt1 と同じです。

```bash
bin/recfriio [オプション] チャンネル 秒 出力先
```

| 引数 | 意味 |
|---|---|
| チャンネル | `13`〜`62`（UHF）、`C13`〜`C63`（CATV。`K13` とも書ける。UT100B だけ）、`BS01_0`〜`BS23_7`・`CS2`〜`CS24`（U3S だけ） |
| 秒 | 録る長さ。`-` なら止められる（SIGINT・SIGTERM）まで |
| 出力先 | 書き出すファイル。`-` なら標準出力 |

```bash
bin/recfriio 21 30 out.ts                  # 地デジ。空いている機器で受ける
bin/recfriio BS15_0 30 out.ts              # BS・CS は U3S で受ける
bin/recfriio --hdp --b25 27 30 nhk.ts      # UT100B に決めて、内蔵の B-CAS でスクランブルを外す
bin/recfriio --b25 27 - -                  # 標準出力に流し続ける（Mirakurun などから）
```

### 機種の決まり方

次の順で決めます。

1. `--hdp`・`--u3s`（`--mtvu3s` とも書ける）
2. `--device` で渡した機器の USB ID
3. チャンネル（BS・CS は U3S だけ、CATV は UT100B だけ）

どれでも決まらなければ、受けられる機種のうち、ほかのプロセスが使っていないものを UT100B → U3S の順に開きます。

### オプション

| オプション | 意味 |
|---|---|
| `--hdp` / `--u3s`（`--mtvu3s`） | 機種を UT100B / U3S に決める（`recut100b`・`recmtvu3s` には無い） |
| `--b25` | 録画に使う UT100B または U3S の内蔵 B-CAS スロットでスクランブルを外す |
| `--pcsc` | `--b25` で外付けのカードリーダーを使う（`--b25` も付いたことになる。pcscd の窓口は `PCSCLITE_CSOCK_NAME` で変えられる） |
| `--stats` | 終わりに TS のヘッダーを数えた結果（同期・TEI・連続カウンター・PID・PAT の TSID）を書く |
| `--round N` | MULTI2 の段数（既定 4） |
| `--strip` | ヌルパケットを捨てる |
| `--sync` | `--b25` で、鍵がそろうまでの失敗を数えない |
| `--device PATH` | 使う機器（`/dev/bus/usb/BBB/DDD`）。既定は空いているもの |
| `--raw FILE` | B25 を通す前の TS も書き出す（照合用） |
| `--card-log FILE` | B-CAS カードとのやり取りを書き出す（照合用。鍵が入るので人に渡さない） |
| `--firmware FILE` | （UT100B だけ）ファームの無い UT100B しか無ければ、これを送ってから使う |
| `--no-settle` | （U3S だけ）受け始めの、復調部が落ち着く前のパケットも捨てずに書き出す |
| `--no-descramble` | （U3S だけ）USB 転送の暗号を外さずに書き出す（照合用） |

環境変数 `RECFRIIO_USB_TRACE=1`（`RECUT100B_USB_TRACE=1` も使える）を付けると、UT100B に送る制御転送を全部、標準エラーに書きます。

## C/C++ 版の recfriio との違い

- 対象は QRS-UT100B と MonsterTV U3S だけです。`--hdus` など、ほかの機種は使えません。
- `--udp`・`--http`・`--sid`・`--lnb`・`--lockfile` はありません。
- B25 の処理は arib25 をそのまま移しましたが、失敗したときの扱いを 2 つ変えています（`internal/b25/b25.go` の頭）。
- U3S の衛星で、TMCC の一覧に無い TS を頼むとエラーで止まります。C/C++ 版は黙って別の TS を出していました。

## 試験

```bash
go test ./...
```

実機の無いところで回せる試験です。  
UT100B は C/C++ 版が送った制御転送の記録と、U3S は C/C++ 版のチップのクラスで取った I2C の記録と、1 回ずつ同じかを比べます。  
B25・MULTI2・カードとの通信は、arib25 の出力や、作り物のカード・pcscd で確かめます。  
実機で確かめた結果は、機種ごとの docs にあります。

## 資料

- [docs/QRS-UT100B.md](docs/QRS-UT100B.md): UT100B の使い方、確かめた結果、中身（ASV5211・ASIE5606・復調部・チューナー）を調べた結果
- [docs/SK-MTVU3S.md](docs/SK-MTVU3S.md): U3S の使い方、確かめた結果、中身と制御の手順
- [docs/modernization_design.md](docs/modernization_design.md): 書き直しの設計と、その後に分かったこと
- `docs/reference/`: 照合に使う記録（C++ 版が UT100B で送った制御転送など）
- `tools/`: 調べるときの道具（ファームの取り出し、TS の解析、arib25 との照合など）

## 由来

- 元は recfriio r5（clworld ほか）と、STZ 版（stz2012・tslroom）の HDUS・HDP・FriioExpress の対応です。
- 過去の改変者の README（Shift_JIS の原文を UTF-8 にしたもの）は [`docs/original_readmes/`](docs/original_readmes/) にあります。
  - [`README.stz`](docs/original_readmes/README.stz): recfriio r5 + α（STZ 版）
  - [`README.HDUS`](docs/original_readmes/README.HDUS): HDUS 対応パッチ
  - [`README.EXPRESS`](docs/original_readmes/README.EXPRESS): FriioExpress 対応パッチ
  - [`README.arib25`](docs/original_readmes/README.arib25): 茂木 和洋 氏の arib25

## ライセンス

作者の違う部分を合わせています。  
ファイルごとの持ち主は `LICENSE` と各ファイルの頭にあります。

- 初期化・選局の手順と値: recfriio（FUKAUMI Naoki、2 条項 BSD）と、その HDP・UT100B の対応（tslroom、stz2012）
- B25・MULTI2・B-CAS の命令: arib25（茂木 和洋）の移植
- MonsterTV U3S の部分（`internal/em28178`・`tc90522`・`fc0013`・`qm1d1c0042`・`mtvu3s`・`i2c`・`tscheck`）: waya0125（MIT。移植元の recfriio の U3S の部分も MIT）
- それ以外（`tools/`・`firmware/`・`docs/` を含む）: waya0125（MIT）
