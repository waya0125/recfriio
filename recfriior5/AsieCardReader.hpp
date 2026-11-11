// SPDX-License-Identifier: MIT
// Copyright (c) 2026 KASHIWAYA Rikuto
//
// QRS-UT100B の内蔵 B-CAS スロット（ASICEN ASIE5606）を、ASV5211 の I2C 経由で T=1 で使う。
// arib25 の B_CAS_CARD_TRANSPORT として渡せるので、外付けのカードリーダーなしで --b25 が使える。
//
// 手順は coax（github.com/Till0196/coax、Apache-2.0 OR GPL-2.0-or-later）の
// internal/coaxd/smartcard/asie5606 で調べられたものに従った（コードは書き下ろし）。

#ifndef _ASIE_CARD_READER_HPP_
#define _ASIE_CARD_READER_HPP_

#include <stdint.h>
#include <vector>
#include <ostream>

#include <memory>

// arib25 の B_CAS_CARD_TRANSPORT（b_cas_card.h）。B25Decoder.hpp の仮の型とぶつからないよう名前だけ出す
struct B_CAS_CARD_TRANSPORT_;

class AsieCardReader {
public:
	// fd: ASV5211 の usbfs のファイル記述子、address: ASIE5606 の I2C アドレス（0x4a か 0x5a）
	AsieCardReader(int fd, uint8_t address, std::ostream *log);

	// カードの電源を入れて ATR を受け取り、T=1 のやり取りを始める。成功で true
	bool reset(std::vector<uint8_t> *atr = NULL);

	// APDU を 1 つ送り、応答（SW1 SW2 を含む）を受け取る。成功で true
	bool transmit(const uint8_t *apdu, int length, std::vector<uint8_t> *response);

	~AsieCardReader();
	AsieCardReader(const AsieCardReader &) = delete;
	AsieCardReader &operator=(const AsieCardReader &) = delete;

	// arib25 に渡す口（B_CAS_CARD_TRANSPORT*）
	const void *transport() const { return transport_; }

private:
	bool readRegister(uint8_t reg, uint8_t *value);
	bool writeRegister(uint8_t reg, uint8_t value);
	bool command(uint8_t opcode) { return writeRegister(0x00, opcode); }
	bool available(int *count);
	bool wait(int minimum, int polls, int *count);
	bool readWindow(int start, int size, std::vector<uint8_t> *out);
	bool writeWindow(const std::vector<uint8_t> &data);
	bool send(std::vector<uint8_t> block);
	bool receive(bool atr, std::vector<uint8_t> *block);

	static int resetThunk(void *ctx);
	static int transmitThunk(void *ctx, const uint8_t *cmd, int cmd_len, uint8_t *resp, int *resp_len);

	int fd;
	uint8_t address;
	std::ostream *log;
	bool extended;        // ページ切り替えと長さの上位バイトのレジスタがある版か
	uint8_t sequence;     // T=1 の次に送る I ブロックの番号 N(S)
	B_CAS_CARD_TRANSPORT_ *transport_;
};

#endif
