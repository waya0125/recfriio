// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

package bcas

import (
	"bytes"
	"errors"
	"testing"
)

type fakeTransport struct {
	resets   int
	failECM  int
	commands [][]byte
}

func (f *fakeTransport) Reset() error { f.resets++; return nil }

func (f *fakeTransport) Transmit(apdu []byte) ([]byte, error) {
	f.commands = append(f.commands, append([]byte{}, apdu...))
	switch {
	case bytes.Equal(apdu, cmdInitialSetting):
		r := make([]byte, 59)
		r[4], r[5] = 0x21, 0x00
		r[6], r[7] = 0x00, 0x05
		for i := 16; i < 56; i++ {
			r[i] = byte(i)
		}
		r[57], r[58] = 0x90, 0x00
		return r, nil
	case bytes.HasPrefix(apdu, ecmHeader):
		if f.failECM > 0 {
			f.failECM--
			return nil, errors.New("カードが答えない")
		}
		r := make([]byte, 27)
		r[4], r[5] = 0x08, 0x00
		copy(r[6:], "0123456789abcdef")
		return r, nil
	}
	return nil, errors.New("知らない命令")
}

func TestOpenAndECM(t *testing.T) {
	f := &fakeTransport{}
	c, err := Open(f)
	if err != nil {
		t.Fatal(err)
	}
	if c.CASystemID() != 5 || c.SystemKey()[0] != 16 || c.InitCBC()[0] != 48 {
		t.Fatalf("初期設定の読み方が違う")
	}
	ecm := []byte{1, 2, 3}
	key, code, err := c.ProcessECM(ecm)
	if err != nil || code != 0x0800 || string(key) != "0123456789abcdef" {
		t.Fatalf("鍵 %q、番号 %#x、%v", key, code, err)
	}
	want := []byte{0x90, 0x34, 0x00, 0x00, 3, 1, 2, 3, 0}
	if got := f.commands[len(f.commands)-1]; !bytes.Equal(got, want) {
		t.Fatalf("ECM の命令 % x", got)
	}
}

func TestECMRetriesWithReconnect(t *testing.T) {
	f := &fakeTransport{failECM: 2}
	c, err := Open(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ProcessECM([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if f.resets != 3 { // 開いたときの 1 回 + 失敗のたびに 1 回
		t.Fatalf("リセット %d 回", f.resets)
	}
}

func TestECMRetryLimit(t *testing.T) {
	f := &fakeTransport{failECM: 2}
	c, err := OpenWithECMRetries(f, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ProcessECM([]byte{1}); err == nil || f.resets != 2 {
		t.Fatalf("1回の再接続で打ち切らなかった: %d 回, %v", f.resets, err)
	}
	if _, _, err := c.ProcessECM([]byte{2}); err != nil {
		t.Fatalf("次のECMを処理できない: %v", err)
	}
}
