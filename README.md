# recfriio (Modernized Fork with QRS-UT100B Support)

Linux 環境向けの USB デジタルテレビチューナー録画プログラム `recfriio` のモダナイズド・フォークです。  
従来の Friio（黒凡・白凡）および MonsterTV HDUS に加え、**Quixun QRS-UT100B**（ASICEN ASV5211 + ASIE5606）の完全動作および内蔵 B-CAS スロットの直接制御をサポートしています。

---

## 主な特徴・改善点
- **現代的な Linux / GCC / C++ 環境への適合**:
  - Ubuntu 22.04 / 24.04 / 26.04 等の最新ディストリビューションおよびカーネルでのビルド・動作に対応。
- **Quixun QRS-UT100B の完全サポート**:
  - コールドブート（VID: `1738`, PID: `5211`）からのファームウェア投入と再列挙（VID: `3275`, PID: `7020`）。
  - **外付けカードリーダー不要**: ASICEN ASIE5606 スマートカードコントローラの USB ベンダーリクエスト直接制御により、本体内蔵 B-CAS スロットのみでカード認識・APDU 通信を実現。
  - USB URB バッファ最適化と連続 TS 同期バイト検証によるパケット欠落・プチフリの解消。
- **高信頼な受信品質**:
  - PT3・torne（CECH-ZD1 J）・QRS-UT100B の 3 者同時 30 秒録画テストにおいて、**エラー・ドロップ完全ゼロ（⭕ EXCELLENT）** の最高品質を実証済み。

---

## 対応デバイス
| 製品名 | チップセット | オプション | 備考 |
|---|---|---|---|
| Friio (白凡 / 黒凡) | Cypress FX2 | (なし) | 地デジ / BS・CS |
| Friio Express | Cypress FX2 | (なし) | Expressカード型 |
| MonsterTV HDUS / HDUSF | ASV5211 | (なし) | 地デジ |
| **Quixun QRS-UT100B** | ASV5211 + ASIE5606 | `--hdp` | 地デジ / 内蔵 B-CAS スロット対応 |
| MonsterTV HDUC | ASV5211 | `--hdp` | 地デジ |

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
UT100B は電源が入るたびに、ファームの無い状態（`1738:5211`）で起動します。ファームを送ると `3275:7020` として出直します。

ファームはメーカーの著作物なので、このリポジトリには含めていません。**製品付属の CD-ROM にある `Driver/Quixun_AS11Loader.sys` から取り出して使います。** 対応している `.sys` は、ROBRO Origin 版（1.08.11.17）と ROBRO Origin+（forNetbook）版（1.09.02.19）の 2 つです。取り出す際に SHA-256 でファイルと取り出したファームの両方を確かめます。

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

本リポジトリに含まれる各コンポーネントのライセンスは以下の通りです。詳細は `licenses/` ディレクトリ内の各ファイルをご確認ください。

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
