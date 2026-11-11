// SPDX-License-Identifier: MIT
// Copyright (c) 2026 KASHIWAYA Rikuto
//
// QRS-UT100B の内蔵 B-CAS スロット（ASICEN ASIE5606）を T=1 で使う。説明は AsieCardReader.hpp。
//
// ASIE5606 は CCID のリーダーではなく、カードの線へのバイトの通り道を I2C のレジスタとして見せるだけなので、
// T=1 のブロックの組み立て（NAD・PCB・LEN・LRC、続きの受け取り）はここで行う。
// カードの線のビットの並びは逆なので、送るブロックも受けるブロックも 1 バイトずつビットを逆にする。
// LRC は逆にした後のバイトで計算する。

#include "AsieCardReader.hpp"
#include "../arib25/b_cas_card.h"

#include <errno.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>
#include <linux/usbdevice_fs.h>
#include <linux/usb/ch9.h>

namespace {

// ASIE5606 のレジスタ
const uint8_t REG_COMMAND       = 0x00;
const uint8_t REG_PROTOCOL      = 0x01;
const uint8_t REG_TRANSMIT_LOW  = 0x02;
const uint8_t REG_RECEIVE_LOW   = 0x03;
const uint8_t REG_STATUS        = 0x04; // bit 0: カードあり（読む）、bit 7: カードの電源（書く）
const uint8_t REG_VERSION       = 0x09;
const uint8_t REG_TRANSMIT_HIGH = 0x38;
const uint8_t REG_RECEIVE_HIGH  = 0x39;
const uint8_t REG_WINDOW_PAGE   = 0x3a;
const uint8_t REG_WINDOW        = 0x40; // 0x40..0x7f の 64 バイトの窓
const int     WINDOW_SIZE       = 0x40;

// REG_COMMAND に書く命令
const uint8_t CMD_IDLE        = 0x00;
const uint8_t CMD_PREPARE     = 0x80;
const uint8_t CMD_POWER       = 0x08;
const uint8_t CMD_RESET       = 0x09;
const uint8_t CMD_TRANSMIT    = 0x0a;
const uint8_t CMD_ACKNOWLEDGE = 0x0c;

const uint8_t STATUS_CARD_PRESENT = 0x01;
const uint8_t STATUS_POWER_ON     = 0x80;

// 待ち時間（ms）。カードのいちばん遅い答えには、先頭まで 42 回・残りに 8 回の 50ms の見回りが要る
const int POLL_MS = 50, HEAD_POLLS = 42, TAIL_POLLS = 8;
const int PARK_MS = 10, PREPARE_MS = 100, RESET_MS = 200;
const int TRANSMIT_MS = 100; // ブロックを線に送り出す時間。これより前に見ると前の答えの長さが見える

const int MAX_INFORMATION = 254;

// ASV5211 のベンダーリクエスト（Hdp.cpp と同じ）
const uint8_t ASV_REG_READ  = 0x02;
const uint8_t ASV_REG_WRITE = 0x03;

uint8_t reverse8(uint8_t b)
{
	b = (b & 0xf0) >> 4 | (b & 0x0f) << 4;
	b = (b & 0xcc) >> 2 | (b & 0x33) << 2;
	b = (b & 0xaa) >> 1 | (b & 0x55) << 1;
	return b;
}

// ブロックをカードのビットの並びにして、LRC を付ける
std::vector<uint8_t> frame(std::vector<uint8_t> block)
{
	uint8_t lrc = 0;
	for (size_t i = 0; i < block.size(); i++) {
		block[i] = reverse8(block[i]);
		lrc ^= block[i];
	}
	block.push_back(lrc);
	return block;
}

} // namespace

AsieCardReader::AsieCardReader(int fd_, uint8_t address_, std::ostream *log_)
	: fd(fd_), address(address_), log(log_), extended(true), sequence(0)
{
	transport_ = new B_CAS_CARD_TRANSPORT;
	transport_->ctx = this;
	transport_->reset = resetThunk;
	transport_->transmit = transmitThunk;
}

AsieCardReader::~AsieCardReader()
{
	delete transport_;
}

bool
AsieCardReader::readRegister(uint8_t reg, uint8_t *value)
{
	uint8_t data[2] = { 0, 0 };
	usbdevfs_ctrltransfer ctrl = {
		USB_DIR_IN | USB_TYPE_VENDOR | USB_RECIP_DEVICE, ASV_REG_READ,
		(uint16_t)((reg << 8) | address), 0, 2, 1000, data
	};
	if (ioctl(fd, USBDEVFS_CONTROL, &ctrl) != 2 || data[0] != 1) return false;
	*value = data[1];
	return true;
}

bool
AsieCardReader::writeRegister(uint8_t reg, uint8_t value)
{
	uint8_t data[2] = { 0, 0 };
	usbdevfs_ctrltransfer ctrl = {
		USB_DIR_IN | USB_TYPE_VENDOR | USB_RECIP_DEVICE, ASV_REG_WRITE,
		(uint16_t)((reg << 8) | address), value, 2, 1000, data
	};
	return ioctl(fd, USBDEVFS_CONTROL, &ctrl) == 2 && data[0] == 1;
}

// 受け取りの入れ物にたまったバイト数。カードが抜かれていたら失敗
bool
AsieCardReader::available(int *count)
{
	uint8_t high = 0, low = 0, status = 0;
	if (extended && !readRegister(REG_RECEIVE_HIGH, &high)) return false;
	if (!readRegister(REG_RECEIVE_LOW, &low)) return false;
	if (!readRegister(REG_STATUS, &status)) return false;
	if (!(status & STATUS_CARD_PRESENT)) {
		if (log) *log << "B-CAS: card removed" << std::endl;
		return false;
	}
	*count = low | (high & 1) << 8;
	return true;
}

bool
AsieCardReader::wait(int minimum, int polls, int *count)
{
	for (int i = 0; i < polls; i++) {
		usleep(POLL_MS * 1000);
		if (!available(count)) return false;
		if (*count >= minimum) return true;
	}
	if (log) *log << "B-CAS: no answer of " << minimum << " bytes" << std::endl;
	return false;
}

bool
AsieCardReader::readWindow(int start, int size, std::vector<uint8_t> *out)
{
	for (int offset = start; offset < start + size; offset++) {
		int page = offset / WINDOW_SIZE, within = offset % WINDOW_SIZE;
		if (extended && within == 0 && !writeRegister(REG_WINDOW_PAGE, page)) return false;
		uint8_t value;
		if (!readRegister(REG_WINDOW + within, &value)) return false;
		out->push_back(value);
	}
	return true;
}

bool
AsieCardReader::writeWindow(const std::vector<uint8_t> &data)
{
	for (size_t offset = 0; offset < data.size(); offset++) {
		int page = offset / WINDOW_SIZE, within = offset % WINDOW_SIZE;
		if (extended && within == 0 && !writeRegister(REG_WINDOW_PAGE, page)) return false;
		if (!writeRegister(REG_WINDOW + within, data[offset])) return false;
	}
	return true;
}

bool
AsieCardReader::send(std::vector<uint8_t> block)
{
	if (!writeRegister(REG_TRANSMIT_LOW, block.size() & 0xff)) return false;
	if (extended && !writeRegister(REG_TRANSMIT_HIGH, block.size() >> 8)) return false;
	if (!writeWindow(block)) return false;
	if (!command(CMD_TRANSMIT)) return false;
	usleep(TRANSMIT_MS * 1000);
	return true;
}

// ブロックを受け取る（カードのビットの並び・LRC 付きのまま）。ATR は 13 バイトを一度に、
// それ以外は 3 バイト目の長さを見てから残りを受け取る
bool
AsieCardReader::receive(bool atr, std::vector<uint8_t> *block)
{
	int count;
	if (!wait(atr ? 13 : 3, HEAD_POLLS, &count)) return false;
	block->clear();
	if (!readWindow(0, count, block)) return false;
	if (!atr) {
		int total = reverse8((*block)[2]) + 4; // ヘッダー 3 + 情報 + LRC 1
		if (total > count) {
			int second;
			if (!wait(total, TAIL_POLLS, &second)) return false;
			if (!readWindow(count, second - count, block)) return false;
		}
	}
	return command(CMD_ACKNOWLEDGE);
}

bool
AsieCardReader::reset(std::vector<uint8_t> *atr)
{
	uint8_t version = 0;
	if (!readRegister(REG_VERSION, &version)) {
		if (log) *log << "B-CAS: ASIE5606 does not answer at 0x" << std::hex << (int)address << std::dec << std::endl;
		return false;
	}
	version = (version & 0x3e) >> 1;
	extended = version != 0;

	// 前に使った人の途中のやり取りが受け取りの入れ物に残らないよう、いったん止める
	if (!command(CMD_PREPARE)) return false;
	usleep(PARK_MS * 1000);
	if (!command(CMD_IDLE) || !writeRegister(REG_STATUS, 0) || !command(CMD_IDLE)) return false;
	usleep(PREPARE_MS * 1000);

	uint8_t status;
	if (!readRegister(REG_STATUS, &status)) return false;
	if (!(status & STATUS_CARD_PRESENT)) {
		if (log) *log << "B-CAS: no card in the slot" << std::endl;
		return false;
	}

	if (!command(CMD_POWER) || !writeRegister(REG_STATUS, STATUS_POWER_ON)) return false;
	usleep(RESET_MS * 1000);
	if (!command(CMD_RESET)) return false;

	std::vector<uint8_t> raw;
	if (!receive(true, &raw)) return false;
	if (atr) {
		atr->clear();
		for (size_t i = 0; i < raw.size(); i++) atr->push_back(reverse8(raw[i]));
	}

	if (!writeRegister(REG_PROTOCOL, 0x01)) return false; // T=1
	sequence = 0;

	// 受け取れる情報の大きさ（IFSD）を最大の 254 にしてもらう。答えは読まずに捨てる
	if (extended) {
		std::vector<uint8_t> ifs = { 0x00, 0xc1, 0x01, 0xfe };
		if (!send(frame(ifs)) || !command(CMD_ACKNOWLEDGE)) return false;
	}

	if (log) *log << "B-CAS: card ready on the built-in slot (ASIE5606 revision " << (int)version << ")" << std::endl;
	return true;
}

bool
AsieCardReader::transmit(const uint8_t *apdu, int length, std::vector<uint8_t> *response)
{
	int bufferSize = extended ? 0x140 : WINDOW_SIZE;
	int limit = bufferSize - 4 < MAX_INFORMATION ? bufferSize - 4 : MAX_INFORMATION;
	if (length <= 0 || length > limit) return false;

	std::vector<uint8_t> block = { 0x00, (uint8_t)(sequence << 6), (uint8_t)length };
	block.insert(block.end(), apdu, apdu + length);
	block = frame(block);

	response->clear();
	// 続きの印を外さないカードで止まらないよう、回数に上限を付ける
	for (int part = 0; part <= bufferSize / 4; part++) {
		if (!send(block)) return false;
		std::vector<uint8_t> received;
		if (!receive(false, &received)) return false;
		if (received.size() < 4) return false;
		uint8_t lrc = 0;
		for (size_t i = 0; i < received.size(); i++) lrc ^= received[i];
		if (lrc != 0) {
			if (log) *log << "B-CAS: LRC error" << std::endl;
			return false;
		}
		for (size_t i = 0; i < received.size(); i++) received[i] = reverse8(received[i]);
		response->insert(response->end(), received.begin() + 3, received.end() - 1);

		uint8_t pcb = received[1];
		if (!(pcb & 0x20)) {   // 続きなし: 次に送る番号を進める
			sequence ^= 1;
			return true;
		}
		// 続きあり: R ブロックで、次に送ってほしいブロックの番号を返す
		uint8_t next = ((pcb >> 6) & 1) ^ 1;
		std::vector<uint8_t> r = { 0x00, (uint8_t)(0x80 | next << 4), 0x00 };
		block = frame(r);
	}
	return false;
}

int
AsieCardReader::resetThunk(void *ctx)
{
	return static_cast<AsieCardReader *>(ctx)->reset() ? 0 : -1;
}

int
AsieCardReader::transmitThunk(void *ctx, const uint8_t *cmd, int cmd_len, uint8_t *resp, int *resp_len)
{
	std::vector<uint8_t> response;
	if (!static_cast<AsieCardReader *>(ctx)->transmit(cmd, cmd_len, &response)) return -1;
	if ((int)response.size() > *resp_len) return -1;
	memcpy(resp, response.data(), response.size());
	*resp_len = response.size();
	return 0;
}
