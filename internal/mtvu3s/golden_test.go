// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

package mtvu3s

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/waya0125/recfriio/internal/fc0013"
	"github.com/waya0125/recfriio/internal/qm1d1c0042"
	"github.com/waya0125/recfriio/internal/tc90522"
)

// recBus は I2C に送ったものを 1 行ずつ書き、読みには決まった値（counter*37+11）を返す。
// testdata/i2c-golden.txt は、recfriio の Tc90522.cpp・Fc0013.cpp・Qm1d1c0042.cpp に
// 同じ手順をさせて同じ形で書き出したもの（作り方は golden_test.go の下の方）。
type recBus struct {
	out     bytes.Buffer
	counter uint
}

func b2i(stop bool) int {
	if stop {
		return 1
	}
	return 0
}

func (r *recBus) Write(addr uint8, data []byte, stop bool) error {
	fmt.Fprintf(&r.out, "W %02x %x %d\n", addr, data, b2i(stop))
	return nil
}

func (r *recBus) Read(addr uint8, data []byte, stop bool) error {
	for i := range data {
		data[i] = uint8(r.counter*37 + 11)
		r.counter++
	}
	fmt.Fprintf(&r.out, "R %02x %d %d\n", addr, len(data), b2i(stop))
	return nil
}

// TestI2CMatchesRecfriio は、チップの手順が recfriio と同じバイト列を送るかを見る。
// 待ち（ロック待ちの繰り返し）は含めず、1 回ずつ呼ぶ。
func TestI2CMatchesRecfriio(t *testing.T) {
	if testing.Short() {
		t.Skip("FC0013・QM1D1C0042 の待ちで数秒かかる")
	}
	bus := &recBus{}
	ter := tc90522.New(bus, 0x20)
	sat := tc90522.New(bus, 0x22)
	fc := fc0013.New(ter.Repeater(), 0xc6)
	qm := qm1d1c0042.New(sat.Repeater(), 0xc0)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(ter.InitTer())
	must(sat.InitSat())
	must(qm.Init())
	must(fc.Init())
	for _, ch := range []int{13, 21, 27, 33, 34, 40, 52, 62} {
		c, err := ParseChannel(fmt.Sprint(ch))
		must(err)
		fmt.Fprintf(&bus.out, "# UHF %d\n", ch)
		must(sat.SetSatActive(false))
		must(ter.SetTerActive(true))
		must(ter.HoldAGCForTune(true))
		must(fc.Tune(c.FreqKHz, 6))
		must(ter.HoldAGCForTune(false))
		must(ter.ResetTer())
		for range 3 {
			must(fc.AdjustLNA())
			_, err := ter.TerLocked()
			must(err)
		}
		_, _, err = ter.TerCNR()
		must(err)
	}
	fmt.Fprintf(&bus.out, "# FC bw\n")
	must(fc.Tune(473143, 7))
	must(fc.Tune(473143, 8))
	must(fc.Tune(1000000, 6))
	tuneSat := func(c Channel) {
		must(ter.SetTerActive(false))
		must(sat.SetSatActive(true))
		must(qm.Tune(c.FreqKHz))
		must(sat.ResetSat())
		_, err := sat.SatTMCCLocked()
		must(err)
		_, err = sat.SelectSatTSID(c.TSID)
		must(err)
		_, err = sat.SatSlotLocked()
		must(err)
		_, _, err = sat.SatCNR()
		must(err)
	}
	for node := 1; node <= 23; node += 2 {
		c, err := ParseChannel(fmt.Sprintf("BS%02d_0", node))
		must(err)
		fmt.Fprintf(&bus.out, "# BS %d\n", node)
		// 記録は C++ 版の recfriio が頼んでいた TSID（0x4000 | nn<<4 | s、BS15 だけ +1）で取ってある。
		// いまは TMCC の一覧の並びで選ぶ（PickTSID）が、I2C の書き込みの照合にはこの値を渡す。
		c.TSID = uint16(0x4000 | node<<4)
		if node == 15 {
			c.TSID++
		}
		tuneSat(c)
	}
	for n := 2; n <= 24; n += 2 {
		c, err := ParseChannel(fmt.Sprintf("CS%d", n))
		must(err)
		fmt.Fprintf(&bus.out, "# CS %d\n", n)
		c.TSID = 0 // recfriio は CS で TSID を渡さない
		tuneSat(c)
	}

	want, err := os.ReadFile("testdata/i2c-golden.txt")
	must(err)
	got := strings.Split(bus.out.String(), "\n")
	wantLines := strings.Split(string(want), "\n")
	for i := 0; i < len(got) || i < len(wantLines); i++ {
		var g, w string
		if i < len(got) {
			g = got[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			t.Fatalf("%d 行目が違う\n got: %q\nwant: %q", i+1, g, w)
		}
	}
}

// testdata/i2c-golden.txt の作り方:
// recfriio の recfriior5 の Tc90522.cpp・Fc0013.cpp・Qm1d1c0042.cpp・error.cpp と、
// 上の recBus と同じ形で書き出す I2cBus を持つ小さな main（上と同じ手順）を g++ -std=c++17 でまとめて作り、
// その標準出力を保存した（2026-11-19、recfriio 9a391a7）。
