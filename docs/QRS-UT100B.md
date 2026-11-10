# Quixun QRS-UT100B (ASICEN ASV5211 + ASIE5606) 動作仕様 & 検証レポート

## 1. 概要
Quixun QRS-UT100B は、地上デジタルテレビ放送用の USB チューナーです。
内部的には以下のチップセットで構成されています：
- **USB ブリッジ / コントローラ**: ASICEN ASV5211
- **スマートカードコントローラ (内蔵 B-CAS スロット)**: ASICEN ASIE5606
- **チューナー / 復調部**: Newport Media NM120 / Toshiba TC90522 互換系

本リポジトリでは、現代の Linux 環境において QRS-UT100B での高品質 TS 受信および内蔵 B-CAS スロットの直接制御を実証・実現しています。

---

## 2. デバイスの認識とファームウェア展開
UT100B は通電直後は ASICEN のコールドブート状態（VID: `1738`, PID: `5211`）で認識されます。
ファームウェア（`asv5211-firmware.bin`）を転送することで、ウォーム状態の `VidzMedia Quixun HDTV`（VID: `3275`, PID: `7020`）に再列挙されます。

```bash
# ファームウェア投入スクリプト
./firmware/load_firmware.sh
```

---

## 3. 内蔵 B-CAS スロットの制御 (ASICEN ASIE5606)
外付けカードリーダー（SCR3310 等）を一切使わず、本体前面の内蔵カードスロットから B-CAS カードを直接制御できます。

### 通信仕様
- **アクセス方法**: ASV5211 経由の USB ベンダーリクエスト
  - Read: `0x02`, `wValue = (reg << 8) | saddr`
  - Write: `0x03`, `wValue = (reg << 8) | saddr`, `wIndex = val`
  - スマートカードコントローラのスレーブアドレス: `saddr = 0x4a`
- **ATR 取得シーケンス**:
  - レジスタ `0x00 = 0x80` → `0x00` → `0x04 = 0x00` で初期化
  - レジスタ `0x00 = 0x08` (Power ON) → `0x04 = 0x80` → `0x00 = 0x09` (Reset)
  - レジスタ `0x03`/`0x39` で受信長取得後、`0x40` 以降から 13 バイトの ATR をビット反転（LSB-first）して取得
  - 取得される標準 B-CAS ATR: `3B F0 12 00 FF 91 81 B1 7C 45 1F 03 99`
- **APDU 送受信**:
  - T=1 プロトコル準拠でコマンド送信後、カード ID（`90 30 00 00 00`）等を取得可能

```bash
# 内蔵 B-CAS リーダーのテストツール
gcc -O2 tools/probe_bcas.c -o tools/probe_bcas
sudo ./tools/probe_bcas
```

---

## 4. 3 チューナー同時録画品質比較結果
PT3、torne（CECH-ZD1 J）、QRS-UT100B を同一タイミング・同一チャンネル（NHK総合 19ch / 30秒間）で同時受信したパケット品質評価結果です。

```text
================================================================================
 📡 3-Tuner Simultaneous Recording Quality Comparison
================================================================================
Tuner          Size       Bitrate    Duration   SyncErr   TEI Err   CC Drop   Gaps   Judge
--------------------------------------------------------------------------------
PT3            63.1 MB    17.3 Mbps  29.2s      0         0         0         0      ⭕ EXCELLENT
torne          61.0 MB    17.3 Mbps  28.2s      0         0         0         0      ⭕ EXCELLENT
QRS-UT100B     58.5 MB    15.5 Mbps  30.2s      0         0         0         0      ⭕ EXCELLENT
================================================================================
```
- **SyncErr (同期エラー)**: 0
- **TEI Err (RFエラー)**: 0
- **CC Drop (パケット欠落)**: 0
- **Gaps (タイムスタンプ途切れ)**: 0

QRS-UT100B は、PT3 や torne と並び **完全に欠落・エラーのない最高品質（⭕ EXCELLENT）** での同時受信を達成しています。

---

## 5. 将来の近代化計画 (Roadmap)
- [ ] **Go または Rust による完全リライト**:
  - 2008 年の C/C++（Boost 1.74 依存等）からモダン言語へ刷新。
  - **Go 案**: `Till0196/coax` に QRS-UT100B（`asv5211` + `asie5606`）のネイティブバックエンドとして統合。
  - **Rust 案**: `rusb` を用いたクロスプラットフォーム CLI / デーモンとして再実装。
- [ ] **Windows 版の現代化**:
  - レガシーな未署名 .sys ドライバ（XP/Vista 時代）に依存せず、WinUSB 直結で Windows 11 等でも署名問題なく直挿し動作可能にする。
