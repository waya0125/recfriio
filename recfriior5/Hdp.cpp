/*-
 * Copyright (c) 2008 FUKAUMI Naoki.
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 * 1. Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED BY THE AUTHOR ``AS IS'' AND ANY EXPRESS OR
 * IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES
 * OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED.
 * IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY DIRECT, INDIRECT,
 * INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT
 * NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
 * DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
 * THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
 * (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF
 * THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
 */

#include <glib.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include "usbops.hpp"
#include "Hdp.hpp"

#include "AsieDescrambler.hpp"

Hdp::Hdp()
    : AbstractFriio(), saddr( 0 ), buffer( NULL )
{
	supportBands.push_back(BAND_CATV);
	supportBands.push_back(BAND_UHF);
	targetType         = TUNER_HDP;
	asyncBufSize       = HDP_ASYNCBUFFSIZE;
	requestReserveNum  = HDP_REQUEST_RESERVE_NUM;
	requestPollingWait = HDP_REQUEST_POLLING_WAIT;
}


Hdp::~Hdp()
{
    if( initialized ) close();
    if( buffer ) free( buffer );
    buffer = NULL;
}


/*
 * QRS-UT100B の中身と、ここで使うレジスタの名前。
 *
 *   ASV5211（USB ブリッジ） ── I2C ─┬─ 0x20: 復調部（TC90522 の地上波側。C/N の式が合うことで確かめた）
 *                                   │         └ 中継レジスタ 0xfe ─ 0xc0: チューナー（ソニー製と見られる。IC の型番は不明。NM120 ではない）
 *                                   └─ 0x4a / 0x5a: ASIE5606（B-CAS カードの IC。TS のスクランブルも受け持つ）
 *
 * 復調部とチューナーは、ソニーのチューナーモジュール BTD-VK482Z の中にまとめて入っていると見られる
 * （HDUS の基板写真からの推定。docs/QRS-UT100B.md の「概要」）。
 *
 * ASV5211 のベンダーリクエストの名前は coax（internal/coaxd/usb/bridge/asv5211）と、PLEX の Linux ドライバーの
 * ライブラリ（PX-SERIES_ver.1.0_Linux_Driver の libPlexLib_*.a、関数名が残っている）に合わせた。
 */

// ---- ASV5211 のベンダーリクエスト ----
static const uint8_t ASV_REG_READ      = 0x02; // PLEX: ReadI2CData。I2C の機器のレジスタを 1 つ読む: wValue = reg << 8 | addr、答えの [1] が値
static const uint8_t ASV_REG_WRITE     = 0x03; // PLEX: WriteI2CData。1 つ書く: wValue = reg << 8 | addr、wIndex = 値
static const uint8_t ASV_FILTER_READ   = 0x04; // TS フィルター（チャンネル 0）のレジスタを読む
static const uint8_t ASV_FILTER_WRITE  = 0x05; // 書く: wValue = 値の上位 << 8 | reg、wIndex = 値の下位
static const uint8_t ASV_STREAM_START  = 0x06; // wValue = チャンネル
static const uint8_t ASV_STREAM_STOP   = 0x07;
static const uint8_t ASV_GPIO          = 0x08; // wValue = mask << 8 | value
static const uint8_t ASV_FILTER_RESET  = 0x09; // wValue = mode << 8 | チャンネル
static const uint8_t ASV_HIGH_SPEED    = 0x0a; // PLEX: GetDevHighSpeed。USB が高速かを問い合わせるだけ（設定ではない）
static const uint8_t ASV_CUSTOMER_INFO = 0x0c; // 状態 1 バイト + 0x39 バイト
static const uint8_t ASV_I2C_FILL      = 0x0d; // I2C で送るバイトを 3 つずつ溜める: wValue = byte0 << 8 | offset、wIndex = byte2 << 8 | byte1
static const uint8_t ASV_I2C_SEND      = 0x0e; // 溜めたものを送る: wValue = addr
static const uint8_t ASV_GPIO_EX       = 0x10; // 2 つ目の GPIO: wValue = mask << 8 | value
static const uint8_t ASV_SYSCTRL_READ  = 0x17; // PLEX: ReadSysCtrlData。wValue のアドレスから読む（書くのは 0x18）

// ---- I2C のアドレス ----
static const uint8_t I2C_DEMOD = 0x20; // TC90522 地上波側
static const uint8_t I2C_TUNER = 0xc0; // 復調部の中継の先
static const uint8_t I2C_CARD_HDUS = 0x5a, I2C_CARD_HDP = 0x4a; // ASIE5606（どちらで答えるかを確かめて使う）

// ---- TC90522 地上波側のレジスタ（coax の demod/tc90522 と Linux の tc90522 に合わせた）----
static const uint8_t TC_T_AGC_1     = 0x01; // AGC の設定（coax の SetAGCT と同じ組）
static const uint8_t TC_T_1C        = 0x1c; // 制御。PLEX は選局で 0x1c を読んで bit 4・5 を立て、少し待って bit 4 を落とす。
                                            // 元のコードは 0x9d を読んだ値（いつも 0）から 0x1c を作っていて、初期化の表で
                                            // 立てた bit 1 を消していた。0x1c を読んで書き戻す形に直した（9 チャンネルで比べて
                                            // 受信に違いは無かった。2026-11-11）
static const uint8_t TC_T_1E        = 0x1e; // 出力の線の設定と見られる。PLEX は周波数（約 261 MHz の上下）で bit 4・5 を変える
static const uint8_t TC_T_1F        = 0x1f; // 0x1e と組で選局の前後に書く（意味は未確認）
static const uint8_t TC_T_AGC_23    = 0x23; // AGC。bit 0 が自動（0x4d）か止める（0x4c）か
static const uint8_t TC_T_AGC_25    = 0x25; // AGC の設定（coax の SetAGCT と同じ組）
static const uint8_t TC_T_CNDAT     = 0x8b; // C/N の生の値（0x8b..0x8d の 24 ビット）
static const uint8_t TC_T_9D        = 0x9d; // 状態。PLEX は BER・SNR を測るときに最初に読む（ここでは使っていない）
static const uint8_t TC_REPEATER    = 0xfe; // ここに書くと、続くバイトがチューナーへ中継される

// ---- ASIE5606 のレジスタ ----
static const uint8_t CARD_CTRL       = 0x00;
static const uint8_t CARD_04         = 0x04; // 意味は未確認（閉じるときに 0）
static const uint8_t CARD_TS_MODE    = 0x05; // TS のスクランブルの方式: 0x91 = XOR、0x80 = DES
static const uint8_t CARD_IDENTITY   = 0x09; // 下位 3 ビットが 6 なら DES の版（coax も 0x09 で IC を見分ける）
static const uint8_t CARD_TS_KEY     = 0x10; // 0x10..0x1f。coax によると普通の書き込みでは効かない
static const uint8_t CARD_80         = 0x80; // 意味は未確認（閉じるときに 0）

// ---- TS フィルター（チャンネル 0）のレジスタ ----
static const uint8_t FILTER_CONTROL  = 0x40; // bit 0・1 入力、bit 2 モード、bit 3 動かす
static const uint8_t FILTER_XPID_MIN = 0x41; // 捨てる PID の範囲（16 ビット）。0x1fff..0x1fff でヌルパケットだけ捨てる
static const uint8_t FILTER_XPID_MAX = 0x43;

// ---- GPIO（ASV_GPIO の mask・value。どの線が何かは元のコメントからの推測）----
static const uint8_t GPIO_TUNER_POWER = 0x08; // 1 で切る、0 で入れる
static const uint8_t GPIO_CARD_RESET  = 0x11; // ASIE5606 のリセット（1→0→1）。PLEX の基板・coax と同じ線
static const uint8_t GPIO_D4          = 0xd4; // 意味は未確認（切るときに 0）
static const uint8_t GPIO_D5          = 0xd5; // 意味は未確認（入れるときに 0→1）

/*
 * 復調部の初期値（元の名前は tunerAddr / tunerData だったが、書く先は復調部 0x20）。
 */
static const uint8_t demodInitRegs [0x5e] = {

  0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
  0x0C, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
  0x18, 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F,
  0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27,
  0x28, 0x29, 0x2A, 0x2B, 0x2C, 0x2D, 0x2E, 0x2F,
  0x30, 0x31, 0x32, 0x34, 0x38, 0x39, 0x3A, 0x3B,
  0x3C, 0x3D, 0x3E, 0x3F, 0x40, 0x41, 0x42, 0x43,
  0x44, 0x46, 0x47, 0x48, 0x49, 0x4A, 0x4B, 0x4C,
  0x50, 0x51, 0x52, 0x54, 0x55, 0x56, 0x57, 0x58,
  0x5C, 0x5D, 0x5E, 0x5F, 0x70, 0x71, 0x72, 0x75,
  0x76, 0x77, 0x7C, 0x7D, 0xBA, 0xBB, 0xBC, 0xC2,
  0xC7, 0xE4, 0xE6, 0xE9, 0xEC, 0xEF
};

static const uint8_t demodInitValues [0x5e] = {
  0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
  0x00, 0x21, 0x10, 0x03, 0xE0, 0x42, 0x09, 0x20,
  0x29, 0x13, 0x29, 0x13, 0x2A, 0xAA, 0xAA, 0xA8,
  0x00, 0xFF, 0x80, 0x4C, 0x4C, 0x80, 0x00, 0x0C,
  0x60, 0x6B, 0x40, 0x40, 0xFF, 0x00, 0xFF, 0x00,
  0x20, 0x0F, 0x84, 0x0F, 0x00, 0x0F, 0x20, 0x21,
  0x3F, 0x10, 0x08, 0x0C, 0x0C, 0x00, 0x00, 0x4F,
  0xFF, 0x20, 0x00, 0x90, 0xE6, 0x02, 0x54, 0x00,
  0x04, 0x58, 0x20, 0x57, 0xF1, 0x20, 0x70, 0x60,
  0x50, 0x00, 0x01, 0x80, 0x18, 0x00, 0x00, 0x22,
  0x00, 0x01, 0x00, 0x52, 0x00, 0x00, 0x00, 0x10,
  0x00, 0x1A, 0x2F, 0x08, 0x00, 0x00
};

/*
 * チューナー（0xc0）の初期化。型番が分からないので、レジスタは番号のまま。
 */
static const uint8_t tunerInit[][2] = {
    {0x2c, 0x44}, {0x4d, 0x40}, {0x7f, 0x02}, {0x9a, 0x52}, {0x48, 0x5a}, {0x76, 0x1a},
    {0x6a, 0x48}, {0x64, 0x28}, {0x66, 0xe6}, {0x35, 0x11}, {0x7e, 0x01}, {0x0b, 0x99},
    {0x0c, 0x00}, {0x10, 0x40}, {0x12, 0xca}, {0x16, 0x90}, {0x32, 0x36}, {0xd8, 0x18},
    {0x05, 0x01},
};

// チューナーのレジスタ（選局で使うもの）
static const uint8_t TUNER_CONTROL = 0x11; // 0 にしてから周波数を書き、2 で選局を始める（と見られる）
static const uint8_t TUNER_13      = 0x13; // 意味は未確認（選局のたびに 0x15）
static const uint8_t TUNER_FREQ_LO = 0x14; // 周波数（1/64 MHz 単位）の下位
static const uint8_t TUNER_FREQ_HI = 0x15; // 上位

/*
 * I2C の機器のレジスタを 1 つ読む。status に答えの先頭（1 なら成功）を返す。
 */
uint8_t
Hdp::regRead(uint8_t addr, uint8_t reg, uint8_t *status)
{
    uint8_t data[0x200];
    send_ctl(data, tunerFd, ASV_REG_READ, (reg << 8) | addr, 0x0000, 0x0002);
    if (status) *status = data[0];
    return data[1];
}

void
Hdp::regWrite(uint8_t addr, uint8_t reg, uint8_t value)
{
    uint8_t data[0x200];
    send_ctl(data, tunerFd, ASV_REG_WRITE, (reg << 8) | addr, value, 0x0002);
}

void
Hdp::gpio(uint8_t mask, uint8_t value)
{
    uint8_t data[0x200];
    send_ctl(data, tunerFd, ASV_GPIO, (mask << 8) | value, 0x0000, 0x0001);
}

/*
 * 復調部の中継（0xfe）を通して、チューナー（0xc0）へバイト列を送る。
 * I2C で送るのは [0xfe, 0xc0, bytes...]。ASV5211 には 3 バイトずつ溜めてから送る。
 */
void
Hdp::tunerSend(const uint8_t *bytes, int count)
{
    uint8_t frame[2 + 8];
    frame[0] = TC_REPEATER; frame[1] = I2C_TUNER;
    memcpy(frame + 2, bytes, count);
    int len = 2 + count;
    const uint8_t *p = frame;
    uint8_t offset = 0;
    uint8_t data[0x200];

    for (; len >= 3; len -= 3) {
        send_ctl(data, tunerFd, ASV_I2C_FILL, (p[0] << 8) | offset, (p[2] << 8) | p[1], 0x0004);
        p += 3;
        offset += 3;
    }
    switch ( len ) {
        case 2:
            send_ctl(data, tunerFd, ASV_I2C_FILL, (p[0] << 8) | offset, p[1], 0x0003);
            offset += 2;
            break;
        case 1:
            send_ctl(data, tunerFd, ASV_I2C_FILL, (p[0] << 8) | offset, 0x0000, 0x0002);
            offset += 1;
            break;
    }
    send_ctl(data, tunerFd, ASV_I2C_SEND, I2C_DEMOD, 0x0000, offset + 1);
}

void
Hdp::tunerWrite(uint8_t reg, uint8_t value)
{
    const uint8_t bytes[2] = { reg, value };
    tunerSend(bytes, 2);
}

/**
 * 周波数帯域/チャンネルを設定する。
 * @param newBand 周波数帯域 UHF/BS/CS
 * @param newChannel チャンネル
 */
void
Hdp::setChannel(BandType newBand, int newChannel)
{
    if( ! saddr ) return;

    uint8_t data[0x200];
    int freq; // MHz
    const uint16_t xpid_min = 0x1fff, xpid_max = 0x1fff; // ヌルパケットだけ捨てる

    if (BAND_UHF != newBand || newChannel < 13 || newChannel > 62) {
        if(BAND_CATV != newBand || newChannel < 13 || newChannel > 63) {
            throw traceable_error("unknown channel."); // TODO: 適当なエラークラス
        } else {
            // CATV band
            if (newChannel <= 22) {
                freq = 111 + (newChannel - 13) * 6;
            } else if (newChannel == 23) {
                freq = 225;
            } else if (newChannel <= 27) {
                freq = 233 + (newChannel - 24) * 6;
            } else {
                freq = 225 + (newChannel - 23) * 6;
            }
        }
    } else {
        // UHF band
        freq = 473 + (newChannel - 13) * 6;
    }
    if (log) *log << "freq = " << freq << std::endl;

    const uint16_t freqCode = freq * 64; // 1/64 MHz 単位

    /* 選局の前の復調部の設定 */
    regWrite(I2C_DEMOD, TC_T_1C, (regRead(I2C_DEMOD, TC_T_1C) & 0xfb) | 0x38);
    regWrite(I2C_DEMOD, TC_T_AGC_25, 0x00);
    regWrite(I2C_DEMOD, TC_T_AGC_23, 0x4d);
    regWrite(I2C_DEMOD, TC_T_1E, 0xaa);
    regWrite(I2C_DEMOD, TC_T_1F, 0xa8);

    /* チューナーに周波数を書く */
    tunerWrite(TUNER_CONTROL, 0x00);
    tunerWrite(TUNER_13, 0x15);
    tunerWrite(TUNER_FREQ_LO, (uint8_t) freqCode);
    tunerWrite(TUNER_FREQ_HI, (uint8_t) (freqCode >> 8));
    tunerWrite(TUNER_CONTROL, 0x02);

    /* 選局の後の復調部の設定 */
    regWrite(I2C_DEMOD, TC_T_1E, 0xa2);
    regWrite(I2C_DEMOD, TC_T_1F, 0x08);
    regWrite(I2C_DEMOD, TC_T_AGC_1, 0x40);
    regWrite(I2C_DEMOD, TC_T_AGC_23, 0x4c);

    /* TS フィルター（チャンネル 0）: リセットし、入力・動作を有効にして、捨てる PID を決める */
    send_ctl(data, tunerFd, ASV_FILTER_RESET, 0x0100, 0x0000, 0x0001); // mode 1、チャンネル 0
    send_ctl(data, tunerFd, ASV_FILTER_READ, FILTER_CONTROL, 0x0000, 0x0002);
    send_ctl(data, tunerFd, ASV_FILTER_WRITE, ((data[1] | 0x0f) << 8) | FILTER_CONTROL, 0x0000, 0x0002);
    send_ctl(data, tunerFd, ASV_FILTER_WRITE, (xpid_min & 0xff00) | FILTER_XPID_MIN, (xpid_min & 0xff), 0x0003);
    send_ctl(data, tunerFd, ASV_FILTER_WRITE, (xpid_max & 0xff00) | FILTER_XPID_MAX, (xpid_max & 0xff), 0x0003);

    /* 受信を始める（チャンネル 0）*/
    send_ctl(data, tunerFd, ASV_STREAM_START, 0x0000, 0x0000, 0x0001);
    usleep (150 * 1000);
}

/**
 * 信号レベルを取得する。
 * @return float C/N 比（dB）。つかめていなければ 0
 */
const float
Hdp::getSignalLevel(void)
{
    // 復調部の C/N のレジスタ（24 ビット）を読み、Linux の tc90522 ドライバーと同じ式で dB にする。
    uint32_t val = (uint32_t)regRead(I2C_DEMOD, TC_T_CNDAT) << 16;
    val |= (uint32_t)regRead(I2C_DEMOD, TC_T_CNDAT + 1) << 8;
    val |= regRead(I2C_DEMOD, TC_T_CNDAT + 2);

    if (val == 0) {
        if (log) *log << "CNR: register 0 (no lock?)" << std::endl;
        return 0.0f;
    }
    double p = 10.0 * log10(5505024.0 / (double)val);
    double cnr = 0.000024 * p * p * p * p - 0.0016 * p * p * p + 0.0398 * p * p + 0.5491 * p + 3.0965;
    if (cnr < 0) cnr = 0;
    if (log) *log << "CNR: " << cnr << " dB (raw 0x" << std::hex << val << std::dec << ")" << std::endl;
    return (float)cnr;
}


/**
 * データを取得する
 * @param bufp データへのポインタを取得するポインタ(出力)
 *             タイムアウト時NULL
 * @param timeoutMsec データを待つ時間(msec)
 *                    0の場合タイムアウト無し
 * @return int 取得されたデータ長
 */
int
Hdp::getStream(const uint8_t** bufp, int timeoutMsec)
{
    if( ! buffer  ){
        *bufp = NULL;
        return 0;
    }

    const size_t tssize = 188;

    int readsize = AbstractFriio::getStream( bufp, timeoutMsec );
    if( *bufp == NULL ) return readsize;
    if( ! readsize ) return readsize;

    // 受信の始めは、前回の受信の終わりが UT100B の中に残っていて先に出てくる
    // （PCR が前回の録画の終わりとつながる）。決まった量を捨てる。
    if( stale_remaining ){
        size_t drop = (size_t)readsize < stale_remaining ? (size_t)readsize : stale_remaining;
        stale_remaining -= drop;
        *bufp += drop;
        readsize -= drop;
        if( ! readsize ) return 0;
    }

    /* XOR 処理 */

//    fprintf( stderr, "buffersize = %d, remainsize = %d", buffersize, buffer_remainsize );

    // 前回の処理の残りをコピー
    if( buffer_remainsize ){
        memmove( buffer, buffer + ( buffersize - buffer_remainsize ), buffer_remainsize );
        buffersize = buffer_remainsize;
        buffer_remainsize = 0;
    }
    else buffersize = 0;

    memcpy( buffer + buffersize, *bufp, readsize );
    *bufp = buffer;
    buffersize += readsize;

//    fprintf( stderr, " -> buffersize = %d, remainsize = %d", buffersize, buffer_remainsize );

    size_t pos = 0;
    size_t out_len = 0;
    while( 1 ){

        // 同期
        // pos が終わりに来ていたら、読む前に残りとして扱う（buffer[buffersize] を読まない）
        if( pos < buffersize && buffer[ pos ] != 0x47 ){
            size_t pos_sync = pos;
            while( pos < buffersize ){
                if( buffer[ pos ] == 0x47 ){
                    if( pos + tssize < buffersize && buffer[ pos + tssize ] != 0x47 ){
                        ++pos;
                        continue;
                    }
                    if( pos + tssize * 2 < buffersize && buffer[ pos + tssize * 2 ] != 0x47 ){
                        ++pos;
                        continue;
                    }
                    break;
                }
                ++pos;
            }
            if( pos_sync != pos && log ){
                *log << "resync: skipped " << pos - pos_sync << " bytes" << std::endl;
            }
        }

        if( buffersize - pos < tssize ){
            buffer_remainsize = buffersize - pos;
            break;
        }

	// decrypt
	uint8_t tmp[188];
	memcpy(tmp, buffer + pos, 188);
	asieDescramblePacket(tmp, buffer + out_len);

        pos += tssize;
        out_len += tssize;
    }

    *bufp = buffer;
    return out_len;
}


/**
 * デバイスファイルがFriioの物かを確認する。
 * @param devfile デバイスファイル
 * @return Friioである場合true;
 */
bool
Hdp::is_friio(const std::string &devfile)
{
	usb_device_descriptor usb_desc;
	try {
		usb_getdesc(devfile.c_str(), &usb_desc);
	} catch (usb_error &e) {
		if (log) *log << "usb_getdesc: " << e.what() << std::endl;
		return false;
	}
	
	if ( TARGET_ID_VENDOR_HDU == usb_desc.idVendor && TARGET_ID_PRODUCT_HDU == usb_desc.idProduct) {
		if (log) *log << "HDP Type: HDU" << std::endl;
        	tunerCount = 1;
		return true;
	} else if ( TARGET_ID_VENDOR_HDUS == usb_desc.idVendor && TARGET_ID_PRODUCT_FS100U == usb_desc.idProduct) {
		if (log) *log << "HDP Type: LDT-FS100U" << std::endl;
        	tunerCount = 1;
		return true;
	} else if ( TARGET_ID_VENDOR_HDUS == usb_desc.idVendor && (TARGET_ID_PRODUCT_HDP == usb_desc.idProduct || TARGET_ID_PRODUCT_UT100B == usb_desc.idProduct)) {
		if (log) *log << "HDP Type: HDP (QRS-UT100B)" << std::endl;
        	tunerCount = 1;
		return true;
	} else if ( TARGET_ID_VENDOR_HDUS == usb_desc.idVendor && TARGET_ID_PRODUCT_HDP2 == usb_desc.idProduct) {
		if (log) *log << "HDP Type: HDP2" << std::endl;
        	tunerCount = 2;
		return true;
	} else {
		return false;
	}
}


/**
 * 固定処理A(デバイス判定)
 * @param fd 対象ファイルディスクリプタ
 * @return TunerType チューナの種別
 */
TunerType
Hdp::UsbProcFixInitA(int fd)
{
    return TUNER_HDP;
}

/**
 * 初期化処理 : 処理開始
 * 便宜上Beginにしているが、本来どんなことをしているかは不明
 * @param fd 対象ファイルディスクリプタ
 */
void
Hdp::UsbProcBegin(int fd)
{
    if( ! buffer ){
        buffer = (uint8_t*)malloc( HDP_ASYNCBUFFSIZE * TSDATASIZE + 1024 );
    }

    buffersize = 0;
    buffer_remainsize = 0;
    stale_remaining = HDP_STALE_DISCARD_BYTES;

    /* HDP を開く */
    if (log) *log << "opening HDP...";
    uint8_t data[0x200];

    r5 = 0x91;
    saddr = I2C_CARD_HDUS;

    send_ctl(data, fd, ASV_CUSTOMER_INFO, 0x0000, 0x0000, 0x003a);
    send_ctl(data, fd, ASV_SYSCTRL_READ, 0x0002, 0x0000, 0x0003);
    gpio(0xfb, 0x07);
    send_ctl(data, fd, ASV_GPIO_EX, 0x0200, 0x0000, 0x0001); // mask 0x02、value 0

    /* 電源を切る */
    gpio(GPIO_D4, 0x00);
    gpio(GPIO_TUNER_POWER, GPIO_TUNER_POWER);
    usleep (50 * 1000);
    send_ctl(data, fd, ASV_SYSCTRL_READ, 0x0002, 0x0000, 0x0003);

    /* チューナーの電源を入れる */
    gpio(GPIO_TUNER_POWER, GPIO_TUNER_POWER);
    usleep (50 * 1000);
    gpio(GPIO_TUNER_POWER, 0x00);
    usleep (150 * 1000);
    gpio(GPIO_CARD_RESET, 0xff);
    usleep (50 * 1000);
    gpio(GPIO_D5, 0x00);
    usleep (50 * 1000);
    gpio(GPIO_D5, 0xff);
    usleep (100 * 1000);

    /* チューナーと復調部の初期化 */
    const uint8_t tunerReset = 0xff; // チューナーに 1 バイトだけ 0xff を送る（リセットと見られる）
    tunerSend(&tunerReset, 1);
    for (size_t i = 0; i < sizeof(demodInitRegs); i++) {
        regWrite(I2C_DEMOD, demodInitRegs[i], demodInitValues[i]);
    }
    regWrite(I2C_DEMOD, TC_T_1C, (regRead(I2C_DEMOD, TC_T_1C) & 0xeb) | 0x28);
    for (size_t i = 0; i < sizeof(tunerInit) / sizeof(tunerInit[0]); i++) {
        tunerWrite(tunerInit[i][0], tunerInit[i][1]);
    }

    /* ASIE5606 の電源・リセット */
    gpio(GPIO_CARD_RESET, 0xff);
    usleep (50 * 1000);
    gpio(GPIO_CARD_RESET, 0x00);
    usleep (50 * 1000);
    gpio(GPIO_CARD_RESET, 0xff);
    usleep (100 * 1000);

    /* ASIE5606 がどちらのアドレスで答えるか、どの版かを見る */
    uint8_t status;
    uint8_t identity = regRead(saddr, CARD_IDENTITY, &status);
    if (status != 1) {
        saddr = I2C_CARD_HDP;
        identity = regRead(saddr, CARD_IDENTITY, &status);
    }
    if ( (identity & 7) == 6) {
        r5 = 0x80; /* DES */
    }

    send_ctl(data, fd, ASV_HIGH_SPEED, 0x0000, 0x0000, 0x0001);
    usleep (30 * 1000);

    /* TS のスクランブルの方式と鍵（鍵は 0。coax によると、この書き方では効いていない）*/
    regWrite(saddr, CARD_TS_MODE, r5);
    for (uint8_t reg = CARD_TS_KEY; reg < CARD_TS_KEY + 0x10; reg++)
        regWrite(saddr, reg, 0x00);

    card.reset(new AsieCardReader(fd, saddr, log));

    if (log) *log << "done." << std::endl;
}

/**
 * 処理終了
 * @param fd 対象ファイルディスクリプタ
 */
void
Hdp::UsbProcEnd(int fd)
{
    if( ! saddr ) return;

    if (log) *log << "closing HDP...";
    card.reset();

    uint8_t data[0x200];

    /* 受信を止める */
    usleep(100 * 1000);
    send_ctl(data, fd, ASV_STREAM_STOP, 0x0000, 0x0000, 0x0001);
    send_ctl(data, fd, ASV_FILTER_RESET, 0x0100, 0x0000, 0x0001);
    regWrite(saddr, CARD_80, 0x00);
    regWrite(saddr, CARD_04, 0x00);
    regWrite(saddr, CARD_CTRL, 0x00);

    /* 電源を切る */
    usleep (50 * 1000);
    gpio(GPIO_D4, 0x00);
    gpio(GPIO_TUNER_POWER, GPIO_TUNER_POWER);
    usleep (50 * 1000);

    saddr = 0;

    if (log) *log << "done." << std::endl;
}

/**
 * 初期化処理 : ストリーム制御データ
 * @param fd 対象ファイルディスクリプタ
 */
void
Hdp::UsbProcStreamInit(int fd)
{}

/**
 * ＬＥＤ制御
 * @param fd 対象ファイルディスクリプタ
 * @param color 色
 */
void
Hdp::UsbProcLED(int fd, BonLedColor color, bool lnb_powered)
{}

/**
 * 初期化処理 : 固定処理B
 * @param fd 対象ファイルディスクリプタ
 */
void
Hdp::UsbProcFixInitB(int fd)
{}



void
Hdp::send_ctl(uint8_t *data, int fd, uint8_t req, uint16_t val, uint16_t idx, uint16_t len)
{
    usbdevfs_ctrltransfer ctrl = {
        USB_DIR_IN | USB_TYPE_VENDOR | USB_RECIP_DEVICE, req, val, idx, len, REQUEST_TIMEOUT, data
    };
	
    usb_ctrl(fd, &ctrl);

    // RECFRIIO_USB_TRACE=1 で、送った制御転送と返ってきた先頭 2 バイトを標準エラーに出す
    static const bool trace = getenv("RECFRIIO_USB_TRACE") != NULL;
    if (trace) {
        fprintf(stderr, "usb req=%02x val=%04x idx=%04x len=%u -> %02x %02x\n",
                req, val, idx, len, data[0], len > 1 ? data[1] : 0);
    }
}

bool
Hdp::vendorRequest(uint16_t wLength, uint8_t* data, uint8_t bRequest, uint8_t v0, uint8_t v1, uint8_t v2, uint8_t v3)
{
    send_ctl(data, tunerFd, bRequest, (v1 << 8) + v0, (v3 << 8) + v2, wLength);
    return true;
}
