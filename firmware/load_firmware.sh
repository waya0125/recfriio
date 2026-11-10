#!/bin/bash
set -e

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FW="$DIR/asv5211-firmware.bin"

if [ ! -f "$FW" ]; then
    echo "ERROR: Firmware not found at $FW"
    exit 1
fi

echo "Checking for cold ASV52xx device (1738:5211)..."
if ! lsusb -d 1738:5211 >/dev/null 2>&1; then
    if lsusb -d 3275:7020 >/dev/null 2>&1; then
        echo "Device is already warmed up as Quixun HDTV (3275:7020). No loading needed."
        exit 0
    else
        echo "No ASV52xx or Quixun device detected."
        exit 1
    fi
fi

echo "Cold ASV52xx device found. Loading firmware..."
docker run --rm --privileged \
    -v /dev/bus/usb:/dev/bus/usb \
    -v "$FW:/firmware.bin:ro" \
    -v /home/waya/claude-work/torne/trial/bin-0.1.5/coaxd:/coaxd:ro \
    alpine /coaxd load-asv52xx-firmware -firmware /firmware.bin

echo "Waiting for device re-enumeration..."
sleep 2

if lsusb -d 3275:7020 >/dev/null 2>&1; then
    echo "SUCCESS: UT100B re-enumerated as Quixun HDTV (3275:7020)!"
else
    echo "WARNING: Device did not re-enumerate as 3275:7020 yet. Check lsusb."
fi
