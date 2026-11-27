// SPDX-License-Identifier: BSD-2-Clause AND MIT
//
// 区切りの合わせ方（0x47 を探し、188・376 バイト先も 0x47 かを見る）は recfriio の Hdp::getStream
// （recfriio: Copyright (c) 2008 FUKAUMI Naoki、2 条項 BSD。全文は tuner.go の頭）を Go に移したもの。
// 受け始めの古いデータを捨てる処理と、ASIE5606 の USB 転送の DES を外す処理は waya0125（2026、MIT）。

package ut100b

import (
	"fmt"
	"io"

	"github.com/waya0125/recfriio/internal/asie5606"
)

// StaleDiscardBytes は受け始めに捨てるバイト数。
// UT100B の中に前回の受信の終わり（13 KB 以内）が残っていて、受け始めに先に出てくる。
const StaleDiscardBytes = 2 * 16384

const packetSize = asie5606.PacketSize

// Aligner は USB から届いたバイト列を 188 バイトのパケットに区切り、ASIE5606 が USB 転送でかける DES を外す（B25 のスクランブルはそのまま）。
type Aligner struct {
	stale int
	rest  []byte
	d     *asie5606.StreamCipher
	// Log に再同期の記録を書く
	Log io.Writer
	// Resyncs は再同期で読み飛ばした回数
	Resyncs int
}

// NewAligner は受け始めの StaleDiscardBytes を捨てる Aligner を作る。
func NewAligner() *Aligner {
	return &Aligner{stale: StaleDiscardBytes, d: asie5606.NewStreamCipher(), Log: io.Discard}
}

// ResetSync は USB 転送の欠落などでバイト列が途切れたときに呼ぶ。
// 半端な残りを捨てて、次の Feed で 0x47 から合わせ直す。
func (a *Aligner) ResetSync() {
	a.rest = nil
}

// Feed は chunk を受け取り、DES を外したパケットを返す（188 の倍数の長さ）。
// 半端な残りは次の Feed まで持っておく。
func (a *Aligner) Feed(chunk []byte) []byte {
	if a.stale > 0 {
		drop := min(len(chunk), a.stale)
		a.stale -= drop
		chunk = chunk[drop:]
		if len(chunk) == 0 {
			return nil
		}
	}
	buf := append(a.rest, chunk...)
	size := len(buf)
	out := make([]byte, 0, size/packetSize*packetSize)
	pos := 0
	for {
		if pos < size && buf[pos] != 0x47 {
			start := pos
			for pos < size {
				if buf[pos] == 0x47 {
					if pos+packetSize < size && buf[pos+packetSize] != 0x47 {
						pos++
						continue
					}
					if pos+packetSize*2 < size && buf[pos+packetSize*2] != 0x47 {
						pos++
						continue
					}
					break
				}
				pos++
			}
			if pos != start {
				a.Resyncs++
				fmt.Fprintf(a.Log, "resync: skipped %d bytes\n", pos-start)
			}
		}
		if size-pos < packetSize {
			break
		}
		out = out[:len(out)+packetSize]
		a.d.DecryptPacket(out[len(out)-packetSize:], buf[pos:pos+packetSize])
		pos += packetSize
	}
	a.rest = append(a.rest[:0:0], buf[pos:]...)
	return out
}
