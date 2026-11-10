#!/usr/bin/env python3
import sys
import os
import re
from collections import defaultdict

def scan_ts(path, planned_sec=30):
    if not os.path.exists(path):
        return None

    filesize = os.path.getsize(path)
    if filesize == 0:
        return {'filesize': 0, 'packets': 0, 'error': 'Empty file'}

    n = s = e = disc = 0
    cc = {}
    gaps = 0
    wrap = 0
    pcr_pid = None
    first = last = None
    sync_errors = 0

    pid_counts = defaultdict(int)
    pid_drops = defaultdict(int)

    with open(path, 'rb') as f:
        buf = b''
        while True:
            b = f.read(188 * 50000)
            if not b:
                break
            d = buf + b
            i = 0
            if first is None and n == 0:
                i = d.find(b'\x47')
                if i < 0:
                    i = 0

            while i + 188 <= len(d):
                p = d[i:i + 188]
                if p[0] != 0x47:
                    sync_errors += 1
                    i += 1
                    continue
                n += 1
                if p[1] & 0x80:
                    e += 1  # TEI error
                if p[3] & 0xc0:
                    s += 1  # Scrambled

                pid = ((p[1] & 0x1f) << 8) | p[2]
                pid_counts[pid] += 1

                # Continuity counter check
                if pid != 0x1fff and (p[3] & 0x10):
                    c = p[3] & 0xf
                    if pid in cc and c != cc[pid] and c != ((cc[pid] + 1) & 0xf):
                        disc += 1
                        pid_drops[pid] += 1
                    cc[pid] = c

                # PCR timestamp check
                if (p[3] & 0x20) and p[4] >= 7 and (p[5] & 0x10):
                    pcr = ((p[6] << 25) | (p[7] << 17) | (p[8] << 9) | (p[9] << 1) | (p[10] >> 7)) / 90000.0
                    if pcr_pid is None:
                        pcr_pid = pid
                        first = pcr
                    if pid == pcr_pid:
                        pcr += wrap
                        if last is not None and pcr < last - 3600:
                            wrap += (2 ** 33) / 90000.0
                            pcr += (2 ** 33) / 90000.0
                        if last is not None and (pcr - last) > 1.0:
                            gaps += 1
                        last = pcr
                i += 188
            buf = d[i:]

    duration = (last - first) if (first is not None and last is not None) else 0.0
    mbps = (n * 188 * 8 / (duration if duration > 0 else planned_sec)) / 1e6

    return {
        'filesize': filesize,
        'packets': n,
        'duration': duration,
        'mbps': mbps,
        'scrambled': s,
        'errors_tei': e,
        'drops_cc': disc,
        'gaps_pcr': gaps,
        'sync_errors': sync_errors,
        'pid_counts': pid_counts,
        'pid_drops': pid_drops
    }

def print_comparison(results):
    print("\n" + "=" * 80)
    print(" 📡 3-Tuner Simultaneous Recording Quality Comparison")
    print("=" * 80)
    print(f"{'Tuner':<14} {'Size':<10} {'Bitrate':<10} {'Duration':<10} {'SyncErr':<9} {'TEI Err':<9} {'CC Drop':<9} {'Gaps':<6} {'Judge'}")
    print("-" * 80)

    for name, r in results.items():
        if not r or r['packets'] == 0:
            print(f"{name:<14} {'0 B':<10} {'0.0 Mbps':<10} {'0.0s':<10} {'-':<9} {'-':<9} {'-':<9} {'-':<6} ❌ FAILED")
            continue

        size_str = f"{r['filesize'] / 1e6:.1f} MB"
        bitrate_str = f"{r['mbps']:.1f} Mbps"
        dur_str = f"{r['duration']:.1f}s"
        
        is_good = (r['errors_tei'] == 0 and r['drops_cc'] == 0 and r['sync_errors'] == 0 and r['gaps_pcr'] == 0 and r['mbps'] > 10.0)
        judge = "⭕ EXCELLENT" if is_good else ("⚠️ MINOR DROP" if r['drops_cc'] <= 3 and r['errors_tei'] == 0 else "❌ POOR")

        print(f"{name:<14} {size_str:<10} {bitrate_str:<10} {dur_str:<10} {r['sync_errors']:<9} {r['errors_tei']:<9} {r['drops_cc']:<9} {r['gaps_pcr']:<6} {judge}")

    print("-" * 80)
    print("Indicators explained:")
    print("  - SyncErr : Loss of 0x47 framing (Packet alignment failure)")
    print("  - TEI Err : Transport Error Indicator (Hardware RF signal corruption)")
    print("  - CC Drop : Continuity Counter Discontinuity (Buffer overflow / packet loss)")
    print("  - Gaps    : PCR timestamp gap > 1s (Stream starvation or stall)")
    print("=" * 80)

if __name__ == '__main__':
    if len(sys.argv) < 2:
        print("Usage: python3 analyze_ts.py <file1.ts> [file2.ts] ...")
        sys.exit(1)

    results = {}
    for path in sys.argv[1:]:
        name = os.path.basename(path).replace(".ts", "")
        # tidy up name
        if "pt3" in name.lower(): tuner = "PT3"
        elif "torne" in name.lower(): tuner = "torne"
        elif "ut100b" in name.lower(): tuner = "QRS-UT100B"
        else: tuner = name
        results[tuner] = scan_ts(path)

    print_comparison(results)
