#!/bin/bash
set -e

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DIR="${OUT_DIR:-$PWD/comparison}"   # 出力先（OUT_DIR で変えられる）
mkdir -p "$DIR"
cd "$DIR"

export XDG_RUNTIME_DIR="/run/user/1000"

DURATION=30
CH=19

echo "=========================================================="
echo " 📡 Simultaneous 30s Recording Test (ch=$CH: NHK General)"
echo " Tuners: PT3, torne, QRS-UT100B"
echo "=========================================================="

# Check if production recordings are active
RECORDS=$(curl -s "http://localhost:8888/api/recording?isHalfWidth=true" | grep -o '"total":[0-9]*' | cut -d: -f2 || echo "0")
if [ "$RECORDS" != "0" ] && [ -n "$RECORDS" ]; then
    echo "WARNING: Production EPGStation currently has $RECORDS active recordings!"
    echo "Aborting to protect production recording."
    exit 1
fi

# Auto-detect free tuners from coaxd container
echo "Detecting free tuners from coaxd..."
TORNE_TUNER=$(docker exec coaxd coax list --no-start 2>/dev/null | awk '$1 ~ /^cech-zd1j/ && $4 == "free" {print $1; exit}')
PT3_TUNER=$(docker exec coaxd coax list --no-start 2>/dev/null | awk '$1 ~ /^dvb:adapter[13]/ && ($4 == "free" || $5 == "free") {print $1; exit}')

if [ -z "$TORNE_TUNER" ]; then
    echo "ERROR: No free torne tuner found!"
    docker exec coaxd coax list --no-start
    exit 1
fi
if [ -z "$PT3_TUNER" ]; then
    echo "ERROR: No free PT3 ISDB-T tuner found!"
    docker exec coaxd coax list --no-start
    exit 1
fi

echo "Selected tuners: torne=$TORNE_TUNER, PT3=$PT3_TUNER"

rm -f "$DIR/pt3_${CH}.ts" "$DIR/torne_${CH}.ts" "$DIR/ut100b_${CH}.ts"
rm -f "$DIR/pt3.log" "$DIR/torne.log" "$DIR/ut100b.log"

echo "[1/3] Starting torne ($TORNE_TUNER)..."
docker exec -i coaxd timeout -s INT "$DURATION" coax tune --no-start --tuner "$TORNE_TUNER" --ch "$CH" > "$DIR/torne_${CH}.ts" 2>"$DIR/torne.log" &
PID_TORNE=$!

echo "[2/3] Starting PT3 ($PT3_TUNER)..."
docker exec -i coaxd timeout -s INT "$DURATION" coax tune --no-start --tuner "$PT3_TUNER" --ch "$CH" > "$DIR/pt3_${CH}.ts" 2>"$DIR/pt3.log" &
PID_PT3=$!

echo "[3/3] Starting QRS-UT100B (recfriio --hdp)..."
"$HERE/../recfriior5/recfriio" --hdp "$CH" "$DURATION" "$DIR/ut100b_${CH}.ts" > "$DIR/ut100b.log" 2>&1 &
PID_UT100B=$!

echo "Recording in progress (waiting ${DURATION}s)..."
for i in $(seq 1 $DURATION); do
    sleep 1
    printf "."
done
echo ""

echo "Stopping coax tune processes cleanly..."
kill -INT $PID_TORNE 2>/dev/null || true
kill -INT $PID_PT3 2>/dev/null || true
wait $PID_UT100B 2>/dev/null || true
wait $PID_TORNE 2>/dev/null || true
wait $PID_PT3 2>/dev/null || true

echo ""
echo "Files generated:"
ls -lh "$DIR"/*.ts

echo ""
python3 "$HERE/analyze_ts.py" "$DIR/pt3_${CH}.ts" "$DIR/torne_${CH}.ts" "$DIR/ut100b_${CH}.ts"
