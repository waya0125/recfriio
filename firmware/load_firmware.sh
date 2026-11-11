#!/bin/bash
# QRS-UT100B にファームを送る（ファームの無い状態 1738:5211 → 3275:7020）。
#   ./firmware/load_firmware.sh [付属 CD の Quixun_AS11Loader.sys]
# firmware/asv5211-firmware.bin が無ければ、渡された .sys から先に取り出す。
# ファームはメーカーの著作物なので、リポジトリには入れていない（README の「ファームウェア」を参照）。
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TOOL="$DIR/../tools/ut100b-firmware.py"
FW="$DIR/asv5211-firmware.bin"
if [ ! -f "$FW" ]; then
    if [ $# -lt 1 ]; then
        echo "ファーム（$FW）がありません。付属 CD の Driver/Quixun_AS11Loader.sys を引数で渡してください。" >&2
        exit 1
    fi
    python3 "$TOOL" extract "$1" -o "$FW"
fi
python3 "$TOOL" load "$FW"
