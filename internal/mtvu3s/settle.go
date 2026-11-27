// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

package mtvu3s

// Settler は受け始めの、復調部がまだ落ち着いていない間のパケットを捨てる。
// ロックした直後の 1 秒ほどは TEI（直せなかった印）の付いたパケットが続けて出るので、
// TEI も同期の乱れも無いパケットが Clean 個続いたら、そこから先を流す。
// MaxDrop 個捨てても落ち着かなければ、あきらめて、そこから先は捨てずに流す（coax の issue #3 と同じ考え方）。
// 見るのはヘッダー（先頭 2 バイト）だけ。
type Settler struct {
	Clean   int
	MaxDrop int

	run     int // いま続いている、きれいなパケットの数
	pending []byte
	done    bool

	// Dropped は捨てたパケットの数
	Dropped int
	// GaveUp は落ち着かないままあきらめたか
	GaveUp bool
}

// 既定の値。地デジで 1 秒に約 2 万パケット、BS・CS はもっと多い。
const (
	DefaultSettleClean   = 5000   // 0.1〜0.25 秒ぶん
	DefaultSettleMaxDrop = 200000 // 4〜10 秒ぶん
)

// NewSettler は既定の値の Settler を作る。
func NewSettler() *Settler {
	return &Settler{Clean: DefaultSettleClean, MaxDrop: DefaultSettleMaxDrop}
}

// Settled は流し始めたか。
func (s *Settler) Settled() bool { return s.done }

// ResetRun は落ち着く前の連続カウントを戻す（落ち着いたあとは何もしない）。
func (s *Settler) ResetRun() {
	if s.done {
		return
	}
	s.Dropped += s.run
	s.run = 0
	s.pending = s.pending[:0]
}

// Feed は 188 の倍数の長さの packets を受け取り、流してよいものを返す。
// 落ち着くまでは、きれいなパケットが続いている所を手元に持っておき、
// Clean 個そろったら、それも含めて流す（落ち着いた所から先は 1 つも捨てない）。
func (s *Settler) Feed(packets []byte) []byte {
	if s.done {
		return packets
	}
	for p := 0; p+PacketSize <= len(packets); p += PacketSize {
		pkt := packets[p : p+PacketSize]
		if pkt[0] != 0x47 || pkt[1]&0x80 != 0 {
			s.Dropped += s.run + 1
			s.run = 0
			s.pending = s.pending[:0]
		} else {
			s.run++
			s.pending = append(s.pending, pkt...)
		}
		if s.run >= s.Clean {
			s.done = true
			out := append(s.pending, packets[p+PacketSize:]...)
			s.pending = nil
			return out
		}
		if s.Dropped+s.run >= s.MaxDrop {
			s.done = true
			s.GaveUp = true
			out := append(s.pending, packets[p+PacketSize:]...)
			s.pending = nil
			return out
		}
	}
	return nil
}
