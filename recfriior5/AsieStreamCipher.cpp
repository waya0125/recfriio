// SPDX-License-Identifier: MIT
// Copyright (c) 2026 KASHIWAYA Rikuto
//
// ASIE5606 が USB 転送で TS にかける DES を外す（QRS-UT100B / MonsterTV HDUS 系）。
//
// ASIE5606 は、復調器から受けた TS をブリッジ（ASV5211）へ渡すとき、各パケットに DES をかける。
// これは基板の中の IC どうしの転送にかかる暗号で、放送の暗号（ARIB STD-B25、MULTI2）とは別のもの。
// ここで外したあとの TS は、放送のスクランブルがかかったまま。
// 放送のスクランブルを外すのは arib25（--b25）で、ECM とワーク鍵の処理は B-CAS カードが受け持つ。
// 以前は AsieDescrambler という名前だったが、B25 と紛らわしいので変えた。
//
// 外し方（docs/QRS-UT100B.md の「ASIE5606 の USB 転送の暗号」）:
//   1. 4 バイトのヘッダーはそのまま。
//   2. 残りの 184 バイトに、8 バイト周期の値 kWhitening を XOR する。
//   3. 先頭の 128 バイトを DES 鍵 kKeyHead で、残りの 56 バイトを DES 鍵 kKeyTail で、ECB で復号する。
// 鍵は ASIE5606 の 0x10..0x1f が全部 0 のときのもの（recfriio はいつも 0 にしている）。
//
// DES は FIPS 46-3 のとおり。S 箱・置換の表は規格の値をそのまま書き、速さのための
// 「S 箱と P 置換をまとめた表」と鍵スケジュールは、最初に使うときにそこから組み立てる。
// 以前の decrypt.c（「似非 DES」）と、出力が 1 バイトも変わらないことを確かめてある（test_stream_cipher.cpp）。

#include "AsieStreamCipher.hpp"

#include <mutex>

namespace {

const uint8_t kWhitening[8] = { 0x00, 0x00, 0xb1, 0xf2, 0x00, 0x04, 0xf2, 0x04 };
const uint8_t kKeyHead[8]   = { 0xbc, 0x8c, 0x73, 0xe9, 0x0e, 0x68, 0x31, 0x23 };
const uint8_t kKeyTail[8]   = { 0xec, 0x15, 0x80, 0x01, 0x80, 0x7f, 0x80, 0x80 };

// ---- FIPS 46-3 の表 ----

// S 箱（S1..S8、行 0..3 × 列 0..15）
const uint8_t kSBox[8][64] = {
    { 14, 4,13, 1, 2,15,11, 8, 3,10, 6,12, 5, 9, 0, 7,  0,15, 7, 4,14, 2,13, 1,10, 6,12,11, 9, 5, 3, 8,
       4, 1,14, 8,13, 6, 2,11,15,12, 9, 7, 3,10, 5, 0, 15,12, 8, 2, 4, 9, 1, 7, 5,11, 3,14,10, 0, 6,13 },
    { 15, 1, 8,14, 6,11, 3, 4, 9, 7, 2,13,12, 0, 5,10,  3,13, 4, 7,15, 2, 8,14,12, 0, 1,10, 6, 9,11, 5,
       0,14, 7,11,10, 4,13, 1, 5, 8,12, 6, 9, 3, 2,15, 13, 8,10, 1, 3,15, 4, 2,11, 6, 7,12, 0, 5,14, 9 },
    { 10, 0, 9,14, 6, 3,15, 5, 1,13,12, 7,11, 4, 2, 8, 13, 7, 0, 9, 3, 4, 6,10, 2, 8, 5,14,12,11,15, 1,
      13, 6, 4, 9, 8,15, 3, 0,11, 1, 2,12, 5,10,14, 7,  1,10,13, 0, 6, 9, 8, 7, 4,15,14, 3,11, 5, 2,12 },
    {  7,13,14, 3, 0, 6, 9,10, 1, 2, 8, 5,11,12, 4,15, 13, 8,11, 5, 6,15, 0, 3, 4, 7, 2,12, 1,10,14, 9,
      10, 6, 9, 0,12,11, 7,13,15, 1, 3,14, 5, 2, 8, 4,  3,15, 0, 6,10, 1,13, 8, 9, 4, 5,11,12, 7, 2,14 },
    {  2,12, 4, 1, 7,10,11, 6, 8, 5, 3,15,13, 0,14, 9, 14,11, 2,12, 4, 7,13, 1, 5, 0,15,10, 3, 9, 8, 6,
       4, 2, 1,11,10,13, 7, 8,15, 9,12, 5, 6, 3, 0,14, 11, 8,12, 7, 1,14, 2,13, 6,15, 0, 9,10, 4, 5, 3 },
    { 12, 1,10,15, 9, 2, 6, 8, 0,13, 3, 4,14, 7, 5,11, 10,15, 4, 2, 7,12, 9, 5, 6, 1,13,14, 0,11, 3, 8,
       9,14,15, 5, 2, 8,12, 3, 7, 0, 4,10, 1,13,11, 6,  4, 3, 2,12, 9, 5,15,10,11,14, 1, 7, 6, 0, 8,13 },
    {  4,11, 2,14,15, 0, 8,13, 3,12, 9, 7, 5,10, 6, 1, 13, 0,11, 7, 4, 9, 1,10,14, 3, 5,12, 2,15, 8, 6,
       1, 4,11,13,12, 3, 7,14,10,15, 6, 8, 0, 5, 9, 2,  6,11,13, 8, 1, 4,10, 7, 9, 5, 0,15,14, 2, 3,12 },
    { 13, 2, 8, 4, 6,15,11, 1,10, 9, 3,14, 5, 0,12, 7,  1,15,13, 8,10, 3, 7, 4,12, 5, 6,11, 0,14, 9, 2,
       7,11, 4, 1, 9,12,14, 2, 0, 6,10,13,15, 3, 5, 8,  2, 1,14, 7, 4,10, 8,13,15,12, 9, 0, 3, 5, 6,11 },
};

// P 置換（S 箱の出力 32 ビットの並べ替え）
const uint8_t kP[32] = {
    16, 7,20,21,29,12,28,17, 1,15,23,26, 5,18,31,10, 2, 8,24,14,32,27, 3, 9,19,13,30, 6,22,11, 4,25,
};

// 鍵スケジュール: PC-1、PC-2、各ラウンドの左回転の数
const uint8_t kPC1[56] = {
    57,49,41,33,25,17, 9, 1,58,50,42,34,26,18,10, 2,59,51,43,35,27,19,11, 3,60,52,44,36,
    63,55,47,39,31,23,15, 7,62,54,46,38,30,22,14, 6,61,53,45,37,29,21,13, 5,28,20,12, 4,
};
const uint8_t kPC2[48] = {
    14,17,11,24, 1, 5, 3,28,15, 6,21,10,23,19,12, 4,26, 8,16, 7,27,20,13, 2,
    41,52,31,37,47,55,30,40,51,45,33,48,44,49,39,56,34,53,46,42,50,36,29,32,
};
const uint8_t kShifts[16] = { 1, 1, 2, 2, 2, 2, 2, 2, 1, 2, 2, 2, 2, 2, 2, 1 };

inline uint32_t rol(uint32_t v, int n) { return (v << n) | (v >> (32 - n)); }
inline uint32_t ror(uint32_t v, int n) { return (v >> n) | (v << (32 - n)); }

// ---- 速さのための表 ----

// spTable[i][x]: S 箱 i に 6 ビット x を入れ、P 置換をかけ、1 ビット左に回したもの。
// ラウンド関数の右半分を 1 ビット回した形で持つ（そうすると E 拡大がシフトとマスクで済む）ため。
uint32_t spTable[8][64];

// 復号用の鍵スケジュール。ラウンド 16 から 1 の順に、1 ラウンドあたり 2 語。
// 1 語目に S2・S4・S6・S8、2 語目に S1・S3・S5・S7 に入る 6 ビットずつを、上位のバイトから詰める。
struct Schedule { uint32_t words[32]; };
Schedule scheduleHead, scheduleTail;

void buildSpTable()
{
    for (int box = 0; box < 8; box++) {
        for (int x = 0; x < 64; x++) {
            int row = ((x >> 4) & 2) | (x & 1);
            int col = (x >> 1) & 0x0f;
            uint32_t s = (uint32_t)kSBox[box][row * 16 + col] << (28 - 4 * box);
            uint32_t p = 0;
            for (int i = 0; i < 32; i++)
                if ((s >> (32 - kP[i])) & 1) p |= 1u << (31 - i);
            spTable[box][x] = rol(p, 1);
        }
    }
}

Schedule buildDecryptSchedule(const uint8_t key[8])
{
    auto keyBit = [&](int n) { return (key[(n - 1) / 8] >> (7 - (n - 1) % 8)) & 1; }; // n は 1 から
    uint8_t c[28], d[28];
    for (int i = 0; i < 28; i++) { c[i] = keyBit(kPC1[i]); d[i] = keyBit(kPC1[28 + i]); }

    Schedule out = {};
    for (int round = 0; round < 16; round++) {
        for (int s = 0; s < kShifts[round]; s++) {
            uint8_t c0 = c[0], d0 = d[0];
            for (int i = 0; i < 27; i++) { c[i] = c[i + 1]; d[i] = d[i + 1]; }
            c[27] = c0; d[27] = d0;
        }
        uint8_t group[8] = {};
        for (int i = 0; i < 48; i++) {
            int n = kPC2[i];
            uint8_t bit = n <= 28 ? c[n - 1] : d[n - 29];
            group[i / 6] |= bit << (5 - i % 6);
        }
        int slot = 15 - round; // 復号はラウンド 16 から使う
        out.words[2 * slot]     = (uint32_t)group[1] << 24 | group[3] << 16 | group[5] << 8 | group[7];
        out.words[2 * slot + 1] = (uint32_t)group[0] << 24 | group[2] << 16 | group[4] << 8 | group[6];
    }
    return out;
}

void buildTables()
{
    buildSpTable();
    scheduleHead = buildDecryptSchedule(kKeyHead);
    scheduleTail = buildDecryptSchedule(kKeyTail);
}

std::once_flag tablesBuilt;

// ---- DES の 1 ブロック ----

// 2 語の間で、mask の立ったビットを shift だけずらして入れ替える（初期転置・最終転置の部品）
inline void swapBits(uint32_t &a, uint32_t &b, int shift, uint32_t mask)
{
    uint32_t t = ((a >> shift) ^ b) & mask;
    a ^= t << shift;
    b ^= t;
}

inline uint32_t loadBE(const uint8_t *p) { return (uint32_t)p[0] << 24 | p[1] << 16 | p[2] << 8 | p[3]; }
inline void storeBE(uint8_t *p, uint32_t v) { p[0] = v >> 24; p[1] = v >> 16; p[2] = v >> 8; p[3] = v; }

void decryptBlocks(const Schedule &schedule, uint8_t *data, int blocks)
{
    for (; blocks > 0; blocks--, data += 8) {
        uint32_t l = loadBE(data), r = loadBE(data + 4);

        // 初期転置 IP（ビットの入れ替え 5 回で行う）。このあと両半分は 1 ビット左に回した形で持つ
        swapBits(l, r, 4, 0x0f0f0f0f);
        swapBits(l, r, 16, 0x0000ffff);
        swapBits(r, l, 2, 0x33333333);
        swapBits(r, l, 8, 0x00ff00ff);
        r = rol(r, 1);
        { uint32_t t = (l ^ r) & 0xaaaaaaaa; l ^= t; r ^= t; }
        l = rol(l, 1);

        // 16 ラウンド（2 ラウンドずつ 8 回）。E 拡大は「そのまま」と「4 ビット右に回したもの」の 6 ビットずつ
        const uint32_t *k = schedule.words;
        for (int i = 0; i < 8; i++) {
            uint32_t x = r ^ *k++;
            l ^= spTable[1][(x >> 24) & 0x3f] ^ spTable[3][(x >> 16) & 0x3f] ^
                 spTable[5][(x >> 8) & 0x3f] ^ spTable[7][x & 0x3f];
            x = ror(r, 4) ^ *k++;
            l ^= spTable[0][(x >> 24) & 0x3f] ^ spTable[2][(x >> 16) & 0x3f] ^
                 spTable[4][(x >> 8) & 0x3f] ^ spTable[6][x & 0x3f];
            x = l ^ *k++;
            r ^= spTable[1][(x >> 24) & 0x3f] ^ spTable[3][(x >> 16) & 0x3f] ^
                 spTable[5][(x >> 8) & 0x3f] ^ spTable[7][x & 0x3f];
            x = ror(l, 4) ^ *k++;
            r ^= spTable[0][(x >> 24) & 0x3f] ^ spTable[2][(x >> 16) & 0x3f] ^
                 spTable[4][(x >> 8) & 0x3f] ^ spTable[6][x & 0x3f];
        }

        // 最終転置 IP^-1（初期転置の逆順）。最後の入れ替えで左右が入れ替わる
        r = ror(r, 1);
        { uint32_t t = (l ^ r) & 0xaaaaaaaa; l ^= t; r ^= t; }
        l = ror(l, 1);
        swapBits(l, r, 8, 0x00ff00ff);
        swapBits(l, r, 2, 0x33333333);
        swapBits(r, l, 16, 0x0000ffff);
        swapBits(r, l, 4, 0x0f0f0f0f);

        storeBE(data, r);
        storeBE(data + 4, l);
    }
}

} // namespace

void
asieDecryptPacket(const uint8_t *src, uint8_t *dst)
{
    std::call_once(tablesBuilt, buildTables);

    for (int i = 0; i < 4; i++)
        dst[i] = src[i];
    for (int i = 0; i < 184; i++)
        dst[4 + i] = src[4 + i] ^ kWhitening[i & 7];
    decryptBlocks(scheduleHead, dst + 4, 16);       // 128 バイト
    decryptBlocks(scheduleTail, dst + 4 + 128, 7);  // 56 バイト
}
