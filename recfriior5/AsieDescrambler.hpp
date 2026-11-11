// SPDX-License-Identifier: MIT
// Copyright (c) 2026 KASHIWAYA Rikuto
//
// ASIE5606 が USB に流す TS のスクランブル（DES 方式）を解く。中身は AsieDescrambler.cpp を参照。

#ifndef _ASIE_DESCRAMBLER_HPP_
#define _ASIE_DESCRAMBLER_HPP_

#include <stdint.h>

// 188 バイトの TS パケット 1 つを解く。src と dst は同じでも、重ならない別の領域でもよい
// （一部だけ重なるのは不可）。
void asieDescramblePacket(const uint8_t *src, uint8_t *dst);

#endif
