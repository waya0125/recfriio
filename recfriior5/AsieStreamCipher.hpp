// SPDX-License-Identifier: MIT
// Copyright (c) 2026 KASHIWAYA Rikuto
//
// ASIE5606 が USB 転送で TS にかける DES を外す。中身は AsieStreamCipher.cpp を参照。
// 放送のスクランブル（ARIB STD-B25）とは別のもので、外したあとも B25 のスクランブルは残る。

#ifndef _ASIE_STREAM_CIPHER_HPP_
#define _ASIE_STREAM_CIPHER_HPP_

#include <stdint.h>

// 188 バイトの TS パケット 1 つの DES を外す。src と dst は同じでも、重ならない別の領域でもよい
// （一部だけ重なるのは不可）。
void asieDecryptPacket(const uint8_t *src, uint8_t *dst);

#endif
