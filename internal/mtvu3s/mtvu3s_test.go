// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

package mtvu3s

import (
	"bytes"
	"crypto/aes"
	"testing"

	"github.com/waya0125/recfriio/internal/tc90522"
)

func TestParseChannel(t *testing.T) {
	cases := []struct {
		in   string
		band Band
		freq uint32
		tsid uint16
		slot int
	}{
		{"13", BandUHF, 473143, 0, -1},
		{"21", BandUHF, 521143, 0, -1},
		{"62", BandUHF, 767143, 0, -1},
		{"BS01_0", BandBS, 1049480, 0, 0},
		{"bs15_0", BandBS, 1049480 + 7*38360, 0, 0}, // BS15 もずらさない（TMCC の並びで選ぶ）
		{"BS23_2", BandBS, 1049480 + 11*38360, 0, 2},
		{"BS09_1", BandBS, 1049480 + 4*38360, 0, 1},
		{"CS2", BandCS, 1613000, 0x6020, -1},
		{"CS24", BandCS, 1613000 + 11*40000, 0x7180, -1},
		{"CS8", BandCS, 1613000 + 3*40000, 0x6080, -1},
		{"CS10", BandCS, 1613000 + 4*40000, 0x60a0, -1},
	}
	for _, c := range cases {
		got, err := ParseChannel(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got.Band != c.band || got.FreqKHz != c.freq || got.TSID != c.tsid || got.Slot != c.slot {
			t.Errorf("%s = %+v", c.in, got)
		}
	}
	for _, bad := range []string{"12", "63", "BS2_0", "BS25_0", "BS15_8", "BS15", "CS1", "CS26", "C13", "x"} {
		if _, err := ParseChannel(bad); err == nil {
			t.Errorf("%s が通ってしまう", bad)
		}
	}
}

func packet(pid uint16, cc uint8) []byte {
	p := make([]byte, PacketSize)
	p[0] = 0x47
	p[1] = uint8(pid >> 8)
	p[2] = uint8(pid)
	p[3] = 0x10 | cc
	return p
}

type countDescrambler struct{ n int }

func (c *countDescrambler) DescramblePacket(p []byte) { c.n++ }

func TestAligner(t *testing.T) {
	var stream []byte
	stream = append(stream, 0x00, 0x47, 0x12) // ゴミ
	for i := range 10 {
		stream = append(stream, packet(0x100, uint8(i))...)
	}
	a := NewAligner()
	d := &countDescrambler{}
	a.Descrambler = d
	var out []byte
	// 半端な大きさで分けて流す
	for len(stream) > 0 {
		n := min(97, len(stream))
		out = append(out, a.Feed(stream[:n])...)
		stream = stream[n:]
	}
	if len(out) != 10*PacketSize {
		t.Fatalf("%d バイト出た（10 パケットのはず）", len(out))
	}
	if !bytes.Equal(out[:PacketSize], packet(0x100, 0)) {
		t.Fatal("最初のパケットがずれている")
	}
	if a.Skipped != 3 || d.n != 10 || a.BadSync != 0 {
		t.Fatalf("skipped=%d descrambled=%d badsync=%d", a.Skipped, d.n, a.BadSync)
	}
}

func TestAlignerResync(t *testing.T) {
	var stream []byte
	for i := range 4 {
		stream = append(stream, packet(0x100, uint8(i))...)
	}
	stream = append(stream, 0xaa, 0xbb) // 途中で 2 バイト入る
	for i := 4; i < 10; i++ {
		stream = append(stream, packet(0x100, uint8(i))...)
	}
	a := NewAligner()
	var out []byte
	out = append(out, a.Feed(stream[:4*PacketSize+100])...)
	out = append(out, a.Feed(stream[4*PacketSize+100:])...)
	// ずれたまま出たあと、続きでつかみ直す
	var more []byte
	for i := 10; i < 13; i++ {
		more = append(more, packet(0x100, uint8(i))...)
	}
	out = append(out, a.Feed(more)...)
	if a.BadSync == 0 || a.Resyncs != 1 {
		t.Fatalf("badsync=%d resyncs=%d", a.BadSync, a.Resyncs)
	}
	// つかみ直したあとの最後のパケットは正しく出る
	if !bytes.Equal(out[len(out)-PacketSize:], packet(0x100, 12)) {
		t.Fatal("つかみ直したあとのパケットがずれている")
	}
}

func TestCNR(t *testing.T) {
	if got := tc90522.TerCNRFromRaw(0); got != 0 {
		t.Errorf("TerCNRFromRaw(0) = %v", got)
	}
	// 生の値が小さいほど C/N は高い
	if hi, lo := tc90522.TerCNRFromRaw(0x1000), tc90522.TerCNRFromRaw(0x40000); hi <= lo {
		t.Errorf("TerCNRFromRaw: %v <= %v", hi, lo)
	}
	if got := tc90522.SatCNRFromRaw(2999); got != 0 {
		t.Errorf("SatCNRFromRaw(2999) = %v", got)
	}
}

func TestAESDescrambler(t *testing.T) {
	var key [16]byte
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, PacketSize)
	for i := range plain {
		plain[i] = byte(i)
	}
	plain[0] = 0x47

	enc := append([]byte(nil), plain...)
	for b := 0; b < tsAESBlocks; b++ {
		off := tsAESOffset + b*aes.BlockSize
		block.Encrypt(enc[off:off+aes.BlockSize], enc[off:off+aes.BlockSize])
	}

	d, err := NewDescrambler(key)
	if err != nil {
		t.Fatal(err)
	}
	d.DescramblePacket(enc)
	if !bytes.Equal(enc, plain) {
		t.Fatal("AES-128-ECB 復号結果が元のパケットと一致しない")
	}
}

func TestMatchTSID(t *testing.T) {
	// 2026-11-19 の BS23 の実機の一覧（スロット 0・1・3。登録年のビットがスロットごとに違う）
	ids := []uint16{0x4770, 0x4971, 0x4973, 0xfff9}
	cases := []struct {
		want, got uint16
		ok        bool
	}{
		{0x4770, 0x4770, true},  // そのまま
		{0x4171, 0x4971, true},  // BS の登録年のビットを無視して当てる
		{0x4172, 0x4172, false}, // BS23_2 はいま無い
		{0x6020, 0x6020, false}, // CS は登録年を無視しない
		{0, 0, true},
	}
	for _, c := range cases {
		if got, ok := MatchTSID(c.want, ids); got != c.got || ok != c.ok {
			t.Errorf("MatchTSID(%#04x) = %#04x, %v; want %#04x, %v", c.want, got, ok, c.got, c.ok)
		}
	}
	// CS はネットワークのビット（0x1000）だけを無視する（表の 0x7080 と、実際の 0x6080）
	cs := []uint16{0x6080, 0xfff8}
	cases = []struct {
		want, got uint16
		ok        bool
	}{
		{0x6080, 0x6080, true},
		{0x7080, 0x6080, true},
		{0x6280, 0x6280, false}, // 登録年のビットは無視しない
		{0x60a0, 0x60a0, false},
	}
	ids = cs
	for _, c := range cases {
		if got, ok := MatchTSID(c.want, ids); got != c.got || ok != c.ok {
			t.Errorf("MatchTSID(%#04x) = %#04x, %v; want %#04x, %v", c.want, got, ok, c.got, c.ok)
		}
	}
}

// TestPickTSIDBySlot は、BSnn_s の s を TMCC の一覧の並びとして選ぶこと（issue #4）。
// BS09・BS23 の一覧は実機（2026-11-25、issue #4 のログ）、BS15・BS01 は conf から組み立てたもの。期待する TSID は recisdb 1.3.2 の
// dvbv5_channels_isdbs.conf（Mirakurun の channels.yml と同じ名前）の値。
func TestPickTSIDBySlot(t *testing.T) {
	cases := []struct {
		ch   string
		ids  []uint16
		want uint16
		ok   bool
	}{
		{"BS09_0", []uint16{0x4090, 0x4092, 0xfff9}, 0x4090, true},
		{"BS09_1", []uint16{0x4090, 0x4092, 0xfff9}, 0x4092, true}, // 下位ビットで読むと 0x4091 で受からなかった
		{"BS09_2", []uint16{0x4090, 0x4092, 0xfff9}, 0, false},
		{"BS23_0", []uint16{0x4770, 0x4971, 0x4973, 0xfff9}, 0x4770, true},
		{"BS23_1", []uint16{0x4770, 0x4971, 0x4973, 0xfff9}, 0x4971, true},
		{"BS23_2", []uint16{0x4770, 0x4971, 0x4973, 0xfff9}, 0x4973, true}, // 下位ビットで読むと 0x4172 で受からなかった
		{"BS15_0", []uint16{0x40f1, 0x40f2, 0x48f3}, 0x40f1, true},         // BS15 も 1 つずらさない
		{"BS15_2", []uint16{0x40f1, 0x40f2, 0x48f3}, 0x48f3, true},
		{"BS01_1", []uint16{0xfff8, 0x4010, 0x4011, 0x4012}, 0x4011, true}, // 空きは数えない
		{"BS01_0", []uint16{0xfff8}, 0, false},
	}
	for _, c := range cases {
		ch, err := ParseChannel(c.ch)
		if err != nil {
			t.Fatalf("%s: %v", c.ch, err)
		}
		if got, ok := ch.PickTSID(c.ids); got != c.want || ok != c.ok {
			t.Errorf("%s.PickTSID(%#04x) = %#04x, %v; want %#04x, %v", c.ch, c.ids, got, ok, c.want, c.ok)
		}
	}

	// --tsid で TSID を渡したときは、スロットより TSID を優先し、登録年のビットを無視して当てる
	ch, _ := ParseChannel("BS23_0")
	ch.TSID = 0x4173
	if got, ok := ch.PickTSID([]uint16{0x4770, 0x4971, 0x4973}); got != 0x4973 || !ok {
		t.Errorf("--tsid 0x4173 BS23_0 = %#04x, %v", got, ok)
	}
	// CS はこれまでどおり表の TSID
	ch, _ = ParseChannel("CS8")
	if got, ok := ch.PickTSID([]uint16{0x6080, 0xfff8}); got != 0x6080 || !ok {
		t.Errorf("CS8 = %#04x, %v", got, ok)
	}
}

func TestSettler(t *testing.T) {
	s := &Settler{Clean: 3, MaxDrop: 100}
	tei := func(cc uint8) []byte {
		p := packet(0x100, cc)
		p[1] |= 0x80
		return p
	}
	var in []byte
	in = append(in, tei(0)...)
	in = append(in, packet(0x100, 1)...)
	in = append(in, tei(2)...) // ここまで捨てる
	in = append(in, packet(0x100, 3)...)
	in = append(in, packet(0x100, 4)...)
	out := s.Feed(in)
	if out != nil || s.Settled() {
		t.Fatalf("まだ流さないはず: %d バイト", len(out))
	}
	out = s.Feed(append(packet(0x100, 5), packet(0x100, 6)...))
	if !s.Settled() || len(out) != 4*PacketSize || out[3]&0x0f != 3 {
		t.Fatalf("settled=%v len=%d", s.Settled(), len(out))
	}
	if s.Dropped != 3 {
		t.Fatalf("dropped=%d", s.Dropped)
	}
	// 落ち着いたあとは TEI が付いていても全部流す
	if out := s.Feed(tei(7)); len(out) != PacketSize {
		t.Fatal("落ち着いたあとに捨てている")
	}

	g := &Settler{Clean: 1000, MaxDrop: 4}
	var bad []byte
	for i := range 6 {
		bad = append(bad, tei(uint8(i))...)
	}
	if out := g.Feed(bad); !g.GaveUp || len(out) != 2*PacketSize {
		t.Fatalf("gaveup=%v len=%d", g.GaveUp, len(out))
	}

	// ResetRun は落ち着く前の連続カウントだけを戻し、落ち着いたあとは何もしない
	r := &Settler{Clean: 3, MaxDrop: 100}
	if out := r.Feed(append(packet(0x100, 0), packet(0x100, 1)...)); out != nil {
		t.Fatalf("まだ流さないはず: %d バイト", len(out))
	}
	r.ResetRun()
	if r.Dropped != 2 || r.Settled() {
		t.Fatalf("ResetRun 後: dropped=%d settled=%v", r.Dropped, r.Settled())
	}
	var three []byte
	for i := range 3 {
		three = append(three, packet(0x100, uint8(i))...)
	}
	if out := r.Feed(three); !r.Settled() || len(out) != 3*PacketSize {
		t.Fatalf("ResetRun 後の再成立に失敗: settled=%v len=%d", r.Settled(), len(out))
	}
	r.ResetRun()
	if !r.Settled() || r.Dropped != 2 {
		t.Fatalf("落ち着いたあとの ResetRun で状態が変わった: settled=%v dropped=%d", r.Settled(), r.Dropped)
	}
}

func TestAlignerResetSync(t *testing.T) {
	a := NewAligner()
	var head []byte
	for i := range 4 {
		head = append(head, packet(0x100, uint8(i))...)
	}
	// 末尾に半端な 50 バイトを残した状態でギャップが起きる
	head = append(head, packet(0x100, 4)[:50]...)
	out1 := a.Feed(head)
	if len(out1) != 4*PacketSize {
		t.Fatalf("len(out1)=%d, want %d", len(out1), 4*PacketSize)
	}

	a.ResetSync()

	// ギャップ後はゴミ 17 バイトのあとに正常パケットが 4 つ続く
	var tail []byte
	tail = append(tail, make([]byte, 17)...)
	for i := 5; i < 9; i++ {
		tail = append(tail, packet(0x100, uint8(i))...)
	}
	out2 := a.Feed(tail)
	if len(out2) != 4*PacketSize {
		t.Fatalf("len(out2)=%d, want %d", len(out2), 4*PacketSize)
	}
	if a.BadSync != 0 || a.Resyncs != 1 {
		t.Fatalf("badsync=%d resyncs=%d, want 0 and 1", a.BadSync, a.Resyncs)
	}
	if !bytes.Equal(out2[:PacketSize], packet(0x100, 5)) {
		t.Fatal("ResetSync 後の最初のパケットがずれている")
	}
}
