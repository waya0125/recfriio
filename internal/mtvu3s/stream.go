// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

package mtvu3s

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"
)

// PacketSize は TS のパケットの大きさ。
const PacketSize = 188

const (
	tsAESOffset = 12
	tsAESBlocks = 11 // 11 * 16 = 176 バイト (packet[12..187])
)

// Descrambler は、EM28178 が USB 転送でかけた暗号を、188 バイトのパケット 1 つごとにその場で外す。
// どこにどうかかっているかは recfriio の docs/SK-MTVU3S.md。
type Descrambler interface {
	DescramblePacket(packet []byte)
}

type aesDescrambler struct {
	block cipher.Block
}

// NewDescrambler は 16 バイトの鍵を使って AES-128-ECB の Descrambler を作る。
func NewDescrambler(key [16]byte) (Descrambler, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return &aesDescrambler{block: block}, nil
}

func (d *aesDescrambler) DescramblePacket(packet []byte) {
	if len(packet) < PacketSize {
		return
	}
	for b := 0; b < tsAESBlocks; b++ {
		off := tsAESOffset + b*aes.BlockSize
		d.block.Decrypt(packet[off:off+aes.BlockSize], packet[off:off+aes.BlockSize])
	}
}

// Aligner は isochronous で届いたバイト列を 188 バイトのパケットに区切る（Mtvu3s::getStream と同じやり方）。
// つかんでいないうちは、0x47 が 188 バイトおきに 3 つ並ぶ所を探し、そこより前を捨てる。
// つかんだあとは 188 バイトずつ切り出し、先頭が 0x47 でないパケットがあれば、次の Feed でつかみ直す。
type Aligner struct {
	// Descrambler があれば、切り出したパケットごとに呼ぶ
	Descrambler Descrambler
	// Log につかみ直しの記録を書く
	Log io.Writer

	buf    []byte
	synced bool

	// Resyncs はつかみ直した回数（最初につかんだ 1 回は数えない）
	Resyncs int
	// Skipped はつかむために捨てたバイト数
	Skipped int
	// BadSync は先頭が 0x47 でないまま出したパケットの数
	BadSync    int
	everSynced bool
}

// NewAligner は Aligner を作る。
func NewAligner() *Aligner { return &Aligner{Log: io.Discard} }

// ResetSync は USB 転送の欠落などでバイト列が途切れたときに呼ぶ。
// 半端な残りを捨てて、次の Feed で 0x47 の並びからつかみ直す。
func (a *Aligner) ResetSync() {
	a.buf = a.buf[:0]
	a.synced = false
}

// Feed は chunk を受け取り、切り出したパケットを返す（188 の倍数の長さ）。半端は次まで持つ。
func (a *Aligner) Feed(chunk []byte) []byte {
	a.buf = append(a.buf, chunk...)
	if !a.synced {
		off := -1
		for i := 0; i+PacketSize*2 < len(a.buf); i++ {
			if a.buf[i] == 0x47 && a.buf[i+PacketSize] == 0x47 && a.buf[i+PacketSize*2] == 0x47 {
				off = i
				break
			}
		}
		if off < 0 {
			// 見つからなければ、続きと合わせて探せるよう 3 パケットぶんだけ残す
			if keep := PacketSize * 3; len(a.buf) > keep {
				a.Skipped += len(a.buf) - keep
				a.buf = append(a.buf[:0], a.buf[len(a.buf)-keep:]...)
			}
			return nil
		}
		if off > 0 {
			a.Skipped += off
			a.buf = append(a.buf[:0], a.buf[off:]...)
		}
		if a.everSynced {
			a.Resyncs++
			fmt.Fprintf(a.Log, "resync: skipped %d bytes\n", off)
		}
		a.synced = true
		a.everSynced = true
	}

	n := len(a.buf) / PacketSize * PacketSize
	if n == 0 {
		return nil
	}
	out := make([]byte, n)
	copy(out, a.buf[:n])
	a.buf = append(a.buf[:0], a.buf[n:]...)
	for p := 0; p < n; p += PacketSize {
		packet := out[p : p+PacketSize]
		if packet[0] != 0x47 {
			a.synced = false
			a.BadSync++
		}
		if a.Descrambler != nil {
			a.Descrambler.DescramblePacket(packet)
		}
	}
	return out
}
