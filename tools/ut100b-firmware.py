#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 KASHIWAYA Rikuto
"""QRS-UT100B（ASICEN ASV5211）のファームを、付属 CD のドライバーから取り出して送る。

ファームはメーカーの著作物なので、このリポジトリには入れていない。
製品に付いていた CD の Quixun_AS11Loader.sys から、使う人が自分で取り出す。

  取り出す:  ut100b-firmware.py extract <Quixun_AS11Loader.sys> [-o asv5211-firmware.bin]
  送る:      ut100b-firmware.py load [asv5211-firmware.bin] [--index 0x5399]

依存は Python 3 の標準ライブラリだけ（AES の復号もここに書いてある）。
送るのは Linux の usbfs（/dev/bus/usb）に直接。udev で 1738:5211 に書き込み権限を与えるか root で。
"""

import argparse
import ctypes
import fcntl
import glob
import hashlib
import os
import sys
import time

FIRMWARE_SIZE = 0x4000

# 付属 CD の Quixun_AS11Loader.sys。ファームは .data の中に 2 つ、各バイトに 1 を足したうえで
# AES-128-ECB で暗号化されている。ファームの 8 バイト前に起動アドレス（リトルエンディアン 2 バイト）。
# 1 つ目がローダーの送る主のファーム、2 つ目は古い版（PLEX のローダーにも入っている、送られないもの）。
KNOWN_LOADERS = {
    "77244d1065de8893872a1cc5eac94b08c3350c916bc8743904df4a31ddeec6dd": {
        "name": "ROBRO Origin 版 Quixun_AS11Loader.sys 1.08.11.17 (2008-11-17)",
        "images": [0x52D8, 0x92E0],
    },
    "e0438aef67141e2d9e4f0c91f273d979f27ebf17386344479836632969868261": {
        "name": "ROBRO Origin+ (forNetbook) 版 Quixun_AS11Loader.sys 1.09.02.19 (2009-02-19)",
        "images": [0x5558, 0x9560],
    },
}

LOADER_KEY = bytes.fromhex("277883a707d748ed8e12abfb89ccbd77")

# 起動アドレスはファームの版で決まる（機種ではない）。違うアドレスで起動すると抜き差しが要る。
KNOWN_FIRMWARE = {
    "1f70eb5934456b3b78b0c5ff09a1713e55449cfcd40cc3bcc6a9762f00ec08a0": (0x5399, "Quixun 1.08.11.17 の主のファーム"),
    "50d406d570a9357451a1911645bc3ec1bd64c4e59f9919259d3144efafcc38a4": (0x53E4, "Quixun 1.09.02.19 (forNetbook) の主のファーム"),
    "d134890df91427d405ad5619588e63e3220e21f972ede18a9701fec5686b4816": (0x515F, "古い版（Quixun・PLEX の両方のローダーが持つ、送られないもの）"),
    "b45d510200a1690b3ca358d93de13f40e1d3567b663c17e773349ad96f597aa8": (0x5399, "PLEX の Linux ドライバー loader.ko のファーム"),
}

# ブートROMは 4 回のベンダーリクエストでファームを受け取り、最後の 0xac で起動する。
# 0x0c00..0x2000 と 0x2400..0x2800 は送らない（どのファームでも 0 で埋まっている）。
SEGMENTS = [(0x0000, 0x0C00, 0xAB), (0x2000, 0x0400, 0xAB), (0x2800, 0x1000, 0xAB), (0x3800, 0x0800, 0xAC)]
HOLES = [(0x0C00, 0x2000), (0x2400, 0x2800)]

COLD_ID = ("1738", "5211")
WARM_ID = ("3275", "7020")


# ---- AES-128 の復号（FIPS-197 のとおり） ----

def _xtime(a):
    a <<= 1
    return (a ^ 0x11B) & 0xFF if a & 0x100 else a


def _mul(a, b):
    r = 0
    while b:
        if b & 1:
            r ^= a
        a = _xtime(a)
        b >>= 1
    return r


def _sbox():
    sbox = [0] * 256
    p = q = 1
    while True:
        p = p ^ ((p << 1) & 0xFF) ^ (0x1B if p & 0x80 else 0)
        q ^= q << 1
        q ^= q << 2
        q ^= q << 4
        q &= 0xFF
        if q & 0x80:
            q ^= 0x09
        x = q ^ (q << 1 | q >> 7) ^ (q << 2 | q >> 6) ^ (q << 3 | q >> 5) ^ (q << 4 | q >> 4)
        sbox[p] = (x ^ 0x63) & 0xFF
        if p == 1:
            break
    sbox[0] = 0x63
    return sbox


SBOX = _sbox()
INV_SBOX = [0] * 256
for _i, _v in enumerate(SBOX):
    INV_SBOX[_v] = _i


def _expand_key(key):
    w = list(key)
    rcon = 1
    for i in range(16, 176, 4):
        t = w[i - 4:i]
        if i % 16 == 0:
            t = [SBOX[t[1]] ^ rcon, SBOX[t[2]], SBOX[t[3]], SBOX[t[0]]]
            rcon = _xtime(rcon)
        w += [w[i - 16 + j] ^ t[j] for j in range(4)]
    return [w[r * 16:(r + 1) * 16] for r in range(11)]


def _decrypt_block(block, rk):
    s = [b ^ k for b, k in zip(block, rk[10])]
    for rnd in range(9, -1, -1):
        # InvShiftRows（列ごとに並べた state: s[col*4+row]）
        s = [s[((c - r) % 4) * 4 + r] for c in range(4) for r in range(4)]
        s = [INV_SBOX[b] for b in s]
        s = [b ^ k for b, k in zip(s, rk[rnd])]
        if rnd:
            out = []
            for c in range(4):
                a = s[c * 4:c * 4 + 4]
                out += [_mul(a[0], 14) ^ _mul(a[1], 11) ^ _mul(a[2], 13) ^ _mul(a[3], 9),
                        _mul(a[0], 9) ^ _mul(a[1], 14) ^ _mul(a[2], 11) ^ _mul(a[3], 13),
                        _mul(a[0], 13) ^ _mul(a[1], 9) ^ _mul(a[2], 14) ^ _mul(a[3], 11),
                        _mul(a[0], 11) ^ _mul(a[1], 13) ^ _mul(a[2], 9) ^ _mul(a[3], 14)]
            s = out
    return bytes(s)


def aes128_ecb_decrypt(key, data):
    rk = _expand_key(key)
    return b"".join(_decrypt_block(data[i:i + 16], rk) for i in range(0, len(data), 16))


# ---- 取り出し ----

def sha256(data):
    return hashlib.sha256(data).hexdigest()


def looks_like_firmware(image):
    return len(image) == FIRMWARE_SIZE and all(not any(image[a:b]) for a, b in HOLES)


def extract(loader_path, which=0):
    data = open(loader_path, "rb").read()
    digest = sha256(data)
    loader = KNOWN_LOADERS.get(digest)
    if loader is None:
        raise SystemExit(f"{loader_path} は知らないファイルです（SHA-256 {digest}）。"
                         "付属 CD の Driver フォルダーにある Quixun_AS11Loader.sys を渡してください。")
    offset = loader["images"][which]
    index = int.from_bytes(data[offset - 8:offset - 6], "little")
    sealed = bytes((b - 1) & 0xFF for b in data[offset:offset + FIRMWARE_SIZE])
    image = aes128_ecb_decrypt(LOADER_KEY, sealed)
    known = KNOWN_FIRMWARE.get(sha256(image))
    if known is None or known[0] != index or not looks_like_firmware(image):
        raise SystemExit("取り出したファームが想定と違います（復号の誤り）。書き出さずに止めます。")
    return loader["name"], image, index, known[1]


# ---- 送る（usbfs） ----

class CtrlTransfer(ctypes.Structure):
    _fields_ = [("bRequestType", ctypes.c_uint8), ("bRequest", ctypes.c_uint8),
                ("wValue", ctypes.c_uint16), ("wIndex", ctypes.c_uint16),
                ("wLength", ctypes.c_uint16), ("timeout", ctypes.c_uint32),
                ("data", ctypes.c_void_p)]


def _iowr(type_, nr, size):
    return (3 << 30) | (size << 16) | (ord(type_) << 8) | nr


USBDEVFS_CONTROL = _iowr("U", 0, ctypes.sizeof(CtrlTransfer))


def find_devices(vid_pid):
    found = []
    for d in glob.glob("/sys/bus/usb/devices/*"):
        try:
            ids = (open(d + "/idVendor").read().strip(), open(d + "/idProduct").read().strip())
            if ids == vid_pid:
                bus, dev = int(open(d + "/busnum").read()), int(open(d + "/devnum").read())
                found.append(f"/dev/bus/usb/{bus:03d}/{dev:03d}")
        except OSError:
            pass
    return found


def control_out(fd, request, value, index, payload, timeout_ms=1000):
    buf = ctypes.create_string_buffer(bytes(payload), len(payload))
    xfer = CtrlTransfer(0x40, request, value, index, len(payload), timeout_ms,
                        ctypes.cast(buf, ctypes.c_void_p))
    return fcntl.ioctl(fd, USBDEVFS_CONTROL, xfer)


def load(image, index):
    cold = find_devices(COLD_ID)
    if not cold:
        if find_devices(WARM_ID):
            print("UT100B はもうファームが入った状態（3275:7020）です。入れ直すには抜き差ししてください。")
            return 0
        raise SystemExit("ファームの無い UT100B（1738:5211）が見つかりません。")
    path = cold[0]
    fd = os.open(path, os.O_RDWR)
    try:
        for offset, length, request in SEGMENTS:
            written = control_out(fd, request, offset, index, image[offset:offset + length])
            if written != length:
                raise SystemExit(f"0x{offset:04x} からの送信が {written} バイトで止まりました（{length} バイトのはず）。")
    finally:
        os.close(fd)
    for _ in range(50):
        time.sleep(0.1)
        if find_devices(WARM_ID):
            print(f"送りました。{path} が 3275:7020 として出直しました（起動アドレス 0x{index:04x}）。")
            return 0
    raise SystemExit("送りましたが、5 秒待っても 3275:7020 として出てきません。lsusb で確かめてください。")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    ex = sub.add_parser("extract", help="付属 CD の Quixun_AS11Loader.sys からファームを取り出す")
    ex.add_argument("loader")
    ex.add_argument("-o", "--output", default="asv5211-firmware.bin")
    ex.add_argument("--old", action="store_true", help="主のファームではなく、古い版（送られないもの）を取り出す")
    ld = sub.add_parser("load", help="ファームの無い UT100B にファームを送る")
    ld.add_argument("firmware", nargs="?", default="asv5211-firmware.bin")
    ld.add_argument("--index", type=lambda s: int(s, 0), help="起動アドレス（知らないファームのときだけ）")
    args = ap.parse_args()

    if args.cmd == "extract":
        name, image, index, desc = extract(args.loader, 1 if args.old else 0)
        with open(args.output, "wb") as f:
            f.write(image)
        print(f"{name}\n  → {args.output}: {desc}、起動アドレス 0x{index:04x}、SHA-256 {sha256(image)}")
        return 0

    image = open(args.firmware, "rb").read()
    if not looks_like_firmware(image):
        raise SystemExit(f"{args.firmware} は ASV52xx のファームの形をしていません。")
    known = KNOWN_FIRMWARE.get(sha256(image))
    index = args.index if args.index is not None else (known[0] if known else None)
    if index is None:
        raise SystemExit("知らないファームです。起動アドレスを --index で指定してください（違うと抜き差しが要ります）。")
    if known and args.index is not None and args.index != known[0]:
        print(f"注意: このファームの起動アドレスは 0x{known[0]:04x} のはずですが、0x{args.index:04x} で送ります。", file=sys.stderr)
    return load(image, index)


if __name__ == "__main__":
    sys.exit(main())
