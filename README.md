# recfriio（QRS-UT100B 対応フォーク）

Linux 環境向けの USB デジタルテレビチューナー録画プログラム `recfriio` のフォークです。
STZ 版（Friio・HDUS 系に対応）をもとに、**Quixun QRS-UT100B**（ASICEN ASV5211 + ASIE5606）を動かすための変更と、今の環境でビルドできるようにする変更を加えています。

---

## 主な変更点
- **今の環境でのビルド**: boost をやめて C++17 の標準ライブラリにしました（Ubuntu 26.04 / GCC 15 でビルドを確認）。
- **QRS-UT100B への対応**（`--hdp`）:
  - 付属 CD の `Quixun_AS11Loader.sys` からファームを取り出して送る道具（`tools/ut100b-firmware.py`）。
    ファームはリポジトリに含めていません。
  - 本体内蔵の B-CAS スロットで復号（`--b25`）。
    外付けのカードリーダーは要りません（外付けを使うときは `--pcsc`）。
  - 本当の C/N（信号の質）を読む。
    受け始めに出てくる前回の受信の残りを捨てる。
  - TS のスクランブルを解く処理（旧 `decrypt.c`、「似非 DES」）を、規格どおりの DES と 2 つの鍵で書き直し（`make -C recfriior5 test` で照合できます）。
  - レジスタと命令に名前を付けました（`recfriior5/Hdp.cpp` の先頭）。
- 詳しい仕様と調べた結果は [`docs/QRS-UT100B.md`](docs/QRS-UT100B.md) にあります。

---

## 動作確認済みデバイス
**このフォークの今のコードで実機を確かめたのは、QRS-UT100B の 1 台だけです。**
ほかの機種のコードは STZ 版から引き継いでいますが、ビルドが通ることしか確かめていません。

| 製品名 | チップ | オプション | 今のコードでの確認 |
|---|---|---|---|
| **Quixun QRS-UT100B** | ASV5211 + ASIE5606 | `--hdp` | **確認済み**（下記） |
| MonsterTV HDUC | ASV5211 | `--hdp` | 未確認（同じ `--hdp` の経路。実機なし） |
| MonsterTV HDUS / HDUSF | ASV5211 + ASIE5606 | `--hdus` | 未確認（実機なし） |
| Friio（白・黒） | Cypress FX2 | （なし） | 未確認（実機なし） |
| Friio Express | Cypress FX2 | （なし） | 未確認（実機なし） |

QRS-UT100B で確かめたこと（2026-11-11、Ubuntu 26.04・カーネル 7.0.0-43、地上波 ch19）:

| 確かめたこと | 結果 |
|---|---|
| 受信（5 分・60 秒・30 秒など、ファームは PLEX の Linux ドライバーのもの） | TS の誤り・欠けなし（受け始めの前回の残りは捨てる処理を入れたあと、続けて 4 回受けて欠けなし） |
| C/N | 受かる局で約 32 dB、空きチャンネルで 0 |
| 内蔵 B-CAS で復号（`--b25`、20 秒） | 全パケットのスクランブルが外れ、ffmpeg で映像と音声をデコードできた |
| 内蔵 B-CAS で復号（`--b25`、30 分） | 約 1976 万パケットすべてのスクランブルが外れ、TS の誤り・欠けなし。25 分目の映像もデコードできた |
| PT3・torne と同時に同じ局を 30 秒 | 3 台とも誤り・欠けなし（UT100B はヌルパケットを出さないので、ビットレートの見かけは低い） |

まだ確かめていないこと:
- ファームを送る道具（`tools/ut100b-firmware.py load`）。
  付属 CD の Quixun のファームでの受信。
- `--pcsc`（外付けリーダー）での復号。
  試した環境ではホストに pcscd が無く（コンテナの中だけで動いている）、リーダーにつながらなかった。
  PC/SC の経路のコードは、送受信を 1 つの関数にまとめただけで中身は変えていない。
- Ubuntu 26.04 以外の環境。

---

## ビルド方法

### 必要パッケージ (Ubuntu / Debian)
```bash
sudo apt update
sudo apt install build-essential pkg-config libglib2.0-dev libpcsclite-dev
```

### ビルド手順
```bash
# arib25 のビルド
cd arib25
make

# recfriio 本体のビルド
cd ../recfriior5
make
```

---

## 使い方

### 1. QRS-UT100B のファームウェア初期化 (接続のたびに)
UT100B は電源が入るたびに、ファームの無い状態（`1738:5211`）で起動します。
ファームを送ると `3275:7020` として出直します。

ファームはメーカーの著作物なので、このリポジトリには含めていません。
**製品付属の CD-ROM にある `Driver/Quixun_AS11Loader.sys` から取り出して使います。**
対応している `.sys` は、ROBRO Origin 版（1.08.11.17）と ROBRO Origin+（forNetbook）版（1.09.02.19）の 2 つです。
取り出す際に SHA-256 でファイルと取り出したファームの両方を確かめます。

```bash
# 初回: CD の .sys を渡すと、firmware/asv5211-firmware.bin に取り出してから送る
./firmware/load_firmware.sh /path/to/CD/ROBRO_Origin/Driver/Quixun_AS11Loader.sys
# 2 回目以降
./firmware/load_firmware.sh
```

送るには `/dev/bus/usb` の書き込み権限が要ります（root で実行するか、udev で `1738:5211` と `3275:7020` に権限を与えてください）。

### 2. 内蔵 B-CAS カードスロットの動作確認
本体内蔵スロットに B-CAS カードを挿入した状態で、正常にカード情報が読めるかテストします：
```bash
gcc -O2 tools/probe_bcas.c -o tools/probe_bcas
sudo ./tools/probe_bcas
```

### 3. 録画の実行
```bash
# 基本構文: recfriio [オプション] <チャンネル> <録画秒数> <出力ファイル名>
# 例: NHK総合 (19ch) を 30 秒間録画（スクランブルされたまま）
./recfriior5/recfriio --hdp 19 30 nhk.ts

# 本体内蔵の B-CAS スロットで復号しながら録画（外付けのカードリーダーは不要）
./recfriior5/recfriio --hdp --b25 19 30 nhk.ts

# 外付けのカードリーダー（PC/SC）で復号する場合
./recfriior5/recfriio --hdp --b25 --pcsc 19 30 nhk.ts
```

`/dev/bus/usb` の権限が無い場合は root で実行するか、udev で `3275:7020` に権限を与えてください。

### 4. 複数チューナー同時品質比較テスト
`coaxd` で稼働中の PT3、torne、および QRS-UT100B を同一タイミングで録画し、TS パケット品質（同期エラー、TEI エラー、パケットドロップ、PCR Gaps）を自動測定・比較します：
```bash
sudo ./tools/compare_tuners.sh
```

---

## ディレクトリ構成
```text
.
├── recfriior5/          # recfriio 本体ソースコード
├── arib25/              # ARIB STD-B25 復号ライブラリ
├── as11loader/          # AS11 ファームウェアローダーソース
├── firmware/            # ファーム投入スクリプト（ファーム本体は含まない）
│   └── load_firmware.sh
├── tools/               # 調査・品質検証ツール群
│   ├── ut100b-firmware.py # 付属 CD からのファーム取り出し・投入
│   ├── probe_bcas.c     # 内蔵 B-CAS (ASIE5606) 制御ツール
│   ├── compare_tuners.sh# 3 チューナー同時品質比較スクリプト
│   └── analyze_ts.py    # TS パケット精密解析スクリプト
├── docs/                # 詳細仕様書 & 原文ドキュメント
│   ├── QRS-UT100B.md    # QRS-UT100B 動作仕様 & 品質レポート
│   ├── modernization_design.md # 将来のRust統一モダナイズ詳細設計書
│   └── original_readmes/# 過去バージョン (STZ版, HDUS, arib25) 原文 README
└── licenses/            # 各コンポーネントのライセンスファイル
```

---

## ライセンス

本リポジトリに含まれる各コンポーネントのライセンスは以下の通りです。
詳細は `licenses/` ディレクトリ内の各ファイルをご確認ください。

- **recfriio / as11loader**: [2-Clause BSD License](licenses/LICENSE.recfriio) (Copyright (c) FUKAUMI Naoki)
- **arib25**: [仕様確認テストプログラム ライセンス](licenses/LICENSE.arib25) (Copyright (c) 茂木 和洋)
- **追加ツール群・ドキュメント (`tools/`, `firmware/load_firmware.sh`, `docs/`)**: [MIT License](licenses/LICENSE.mit) (Copyright (c) 2026 KASHIWAYA Rikuto)

---

## 原文ドキュメント
過去の改変者によるオリジナルの README（Shift_JIS / 日本語テキスト）は [`docs/original_readmes/`](docs/original_readmes/) に保存されています。
- [`README.stz`](docs/original_readmes/README.stz): recfriio r5 + α (STZ版) 原文
- [`README.HDUS`](docs/original_readmes/README.HDUS): HDUS 対応パッチ原文
- [`README.EXPRESS`](docs/original_readmes/README.EXPRESS): FriioExpress 対応パッチ原文
- [`README.arib25`](docs/original_readmes/README.arib25): 茂木 和洋 氏による arib25 原文
