// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

package cli

import (
	"io"
	"testing"
)

// recut100b の前の試験と同じもの（UT100B に決めたとき）
func TestParseArgsUT100B(t *testing.T) {
	cases := []struct {
		args    []string
		freq    int
		seconds int
		output  string
		b25     bool
	}{
		{[]string{"19", "60", "out.ts"}, 509, 60, "out.ts", false},
		{[]string{"--b25", "27", "-", "-"}, 557, 0, "-", true},
		{[]string{"-b25", "C13", "10", "x.ts"}, 111, 10, "x.ts", true},
		{[]string{"k63", "10", "x.ts"}, 465, 10, "x.ts", false},
		{[]string{"--pcsc", "19", "10", "x"}, 509, 10, "x", true}, // --pcsc は --b25 も付いたことになる
	}
	for _, c := range cases {
		o, err := parseArgs("recut100b", UT100B, c.args, io.Discard)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if o.kind != UT100B || o.channel.freqMHz != c.freq || o.seconds != c.seconds || o.output != c.output || o.b25 != c.b25 {
			t.Errorf("%v: %+v", c.args, o)
		}
	}
	for _, bad := range [][]string{
		{"12", "10", "x"}, {"19", "0", "x"}, {"19", "x"}, {"B1", "10", "x"},
		{"BS15_0", "10", "x"},      // UT100B は衛星を受けられない
		{"--u3s", "21", "10", "x"}, // 決めてあるときは --hdp・--u3s は無い
	} {
		if _, err := parseArgs("recut100b", UT100B, bad, io.Discard); err == nil {
			t.Errorf("%v が通った", bad)
		}
	}
}

func TestParseArgsRecfriio(t *testing.T) {
	cases := []struct {
		args []string
		kind Kind
		ut   bool
		u3s  bool
	}{
		{[]string{"21", "10", "x"}, Any, true, true},
		{[]string{"--hdp", "21", "10", "x"}, UT100B, true, true},
		{[]string{"--u3s", "BS15_0", "10", "x"}, U3S, false, true},
		{[]string{"--mtvu3s", "CS2", "10", "x"}, U3S, false, true},
		{[]string{"BS15_0", "10", "x"}, Any, false, true},
		{[]string{"C13", "10", "x"}, Any, true, false},
	}
	for _, c := range cases {
		o, err := parseArgs("recfriio", Any, c.args, io.Discard)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if o.kind != c.kind || o.channel.ut100bOK != c.ut || o.channel.u3sOK != c.u3s {
			t.Errorf("%v: kind=%v ut=%v u3s=%v", c.args, o.kind, o.channel.ut100bOK, o.channel.u3sOK)
		}
	}
	if o, _ := parseArgs("recfriio", Any, []string{"BS15_0", "10", "x"}, io.Discard); o.channel.u3s.TSID != 0 || o.channel.u3s.Slot != 0 {
		t.Errorf("BS15_0 の TSID %#04x・スロット %d", o.channel.u3s.TSID, o.channel.u3s.Slot)
	}
	if o, err := parseArgs("recfriio", Any, []string{"--tsid", "0x4092", "BS09_1", "10", "x"}, io.Discard); err != nil || o.channel.u3s.TSID != 0x4092 {
		t.Errorf("--tsid 0x4092 BS09_1: %v", err)
	}
	for _, bad := range [][]string{
		{"--hdp", "BS15_0", "10", "x"},
		{"--u3s", "C13", "10", "x"},
		{"--hdp", "--u3s", "21", "10", "x"},
		{"--tsid", "0x4092", "21", "10", "x"},
		{"--tsid", "zz", "BS09_1", "10", "x"},
		{"--tsid", "0", "BS09_1", "10", "x"},
		{"--hdus", "21", "10", "x"},
		{"BS2_0", "10", "x"},
	} {
		if _, err := parseArgs("recfriio", Any, bad, io.Discard); err == nil {
			t.Errorf("%v が通った", bad)
		}
	}
}

func TestParseArgsU3S(t *testing.T) {
	o, err := parseArgs("recmtvu3s", U3S, []string{"--no-settle", "--no-descramble", "CS2", "-", "-"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.kind != U3S || !o.noSettle || !o.noDescramble || o.channel.u3s.TSID != 0x6020 || o.seconds != 0 {
		t.Errorf("%+v", o)
	}
	for _, bad := range [][]string{
		{"C13", "10", "x"},
		{"--firmware", "fw.bin", "21", "10", "x"}, // U3S にはファームのオプションが無い
	} {
		if _, err := parseArgs("recmtvu3s", U3S, bad, io.Discard); err == nil {
			t.Errorf("%v が通った", bad)
		}
	}
}
