// SPDX-License-Identifier: MIT

package mtvu3s

import (
	"bytes"
	"crypto/aes"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/waya0125/recfriio/internal/bcas"
	"github.com/waya0125/recfriio/internal/em28178"
)

type cardReply struct {
	tx, rx  []byte
	partial bool
}

type cardUSB struct {
	t          *testing.T
	replies    []cardReply
	tx         []byte
	rx         []byte
	partial    bool
	shortRead  bool
	resets     int
	inOffsets  []uint16
	outOffsets []uint16
	waits      []time.Duration
}

func (u *cardUSB) reader() *CardReader {
	return &CardReader{
		readReg: u.read, writeReg: u.write, control: u.control,
		sleep: func(d time.Duration) { u.waits = append(u.waits, d) },
	}
}

func (u *cardUSB) read(reg uint8) (uint8, error) {
	switch reg {
	case cardStatus:
		return cardReadyBits, nil
	case cardMode:
		return 0x04, nil
	case cardLength:
		if u.partial {
			return 8, nil
		}
		return uint8(len(u.rx)), nil
	case cardLenHi:
		return uint8(len(u.rx) >> 8), nil
	default:
		u.t.Fatalf("予期しないレジスタ読み出し: %#x", reg)
		return 0, nil
	}
}

func (u *cardUSB) write(reg, value uint8) error {
	switch reg {
	case em28178.RegGPIOShadow:
		if value != 0 {
			u.t.Fatalf("カードリセット値: %#x", value)
		}
		u.resets++
		u.rx = cardATR[:]
	case cardMode:
		if value != 1 {
			u.t.Fatalf("カード送信モード: %#x", value)
		}
	case cardStatus:
		if value != 0xff {
			u.t.Fatalf("カード状態クリア: %#x", value)
		}
	case cardLength:
		if len(u.replies) == 0 {
			u.t.Fatal("予期しないカード送信")
		}
		reply := u.replies[0]
		u.replies = u.replies[1:]
		if int(value) != len(u.tx) || !bytes.Equal(u.tx, reply.tx) {
			u.t.Fatalf("送信 %x (%d), 期待値 %x", u.tx, value, reply.tx)
		}
		u.rx, u.partial = reply.rx, reply.partial
		u.tx = nil
	default:
		u.t.Fatalf("予期しないレジスタ書き込み: %#x", reg)
	}
	return nil
}

func (u *cardUSB) control(direction, request uint8, value, index uint16, data []byte, timeout uint32) (int, error) {
	if request != 0x14 || value != 0 || len(data) > 64 || timeout != 1000 {
		u.t.Fatalf("バッファの転送条件: %#x %#x %#x %d %d", direction, request, value, len(data), timeout)
	}
	switch direction {
	case 0x40:
		if index != 0x0200+uint16(len(u.tx)) {
			u.t.Fatalf("送信オフセット: %#x", index)
		}
		u.outOffsets = append(u.outOffsets, index)
		u.tx = append(u.tx, data...)
	case 0xc0:
		u.inOffsets = append(u.inOffsets, index)
		u.partial = false
		if u.shortRead {
			return len(data) - 1, nil
		}
		return copy(data, u.rx[int(index):]), nil
	default:
		u.t.Fatalf("転送方向: %#x", direction)
	}
	return len(data), nil
}

func testCardFrame(pcb byte, payload []byte) []byte {
	frame := append([]byte{0, pcb, byte(len(payload))}, payload...)
	return append(frame, cardLRC(frame))
}

func testEncryptedReply(t *testing.T, pcb byte, payload []byte) []byte {
	t.Helper()
	frame := testCardFrame(pcb, payload)
	block, err := aes.NewCipher(make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	block.Encrypt(frame[9:25], frame[9:25])
	return frame
}

func TestCardInitialSettingAndECM(t *testing.T) {
	init := make([]byte, 61)
	init[1], init[4], init[7], init[59] = 57, 0x21, 5, 0x90
	for i := 16; i < 56; i++ {
		init[i] = byte(i)
	}
	ecm := make([]byte, 25)
	ecm[1], ecm[4], ecm[23] = 21, 0x08, 0x90
	copy(ecm[6:], "0123456789abcdef")

	usb := &cardUSB{t: t, replies: []cardReply{
		{tx: []byte{0, 0xc1, 1, 0xfe, 0x3e}, rx: []byte{0, 0xe1, 1, 0xfe, 0x1e}},
		{tx: testCardFrame(0, []byte{0x90, 0x30, 0, 0, 0}), rx: testEncryptedReply(t, 0, init), partial: true},
		{tx: testCardFrame(0x40, []byte{0x90, 0x34, 0, 0, 1, 1, 0}), rx: testEncryptedReply(t, 0x40, ecm)},
	}}
	reader := usb.reader()
	card, err := bcas.Open(reader)
	if err != nil {
		t.Fatal(err)
	}
	if card.CASystemID() != 5 || card.SystemKey()[0] != 16 || card.InitCBC()[0] != 48 {
		t.Fatal("復元した初期設定が不正")
	}
	key, code, err := card.ProcessECM([]byte{1})
	if err != nil || code != 0x0800 || string(key) != "0123456789abcdef" {
		t.Fatalf("ECM応答: %x %#x %v", key, code, err)
	}
	if !slices.Equal(usb.inOffsets, []uint16{0, 0, 0, 0, 64, 0}) ||
		!slices.Equal(usb.waits, []time.Duration{700 * time.Millisecond, 80 * time.Millisecond, 80 * time.Millisecond, 5 * time.Millisecond, 80 * time.Millisecond}) {
		t.Fatalf("転送順が不正: 読み出し位置 %v, 待機 %v", usb.inOffsets, usb.waits)
	}
	if _, _, err := card.ProcessECM(make([]byte, 119)); !errors.Is(err, bcas.ErrAPDULength) || usb.resets != 1 {
		t.Fatalf("APDU長超過でリセットが発生: %d 回, %v", usb.resets, err)
	}
}

func TestCardBadLRCRequiresReset(t *testing.T) {
	apdu := make([]byte, 124)
	plain := []byte{0x6d, 0}
	ifs := cardReply{tx: []byte{0, 0xc1, 1, 0xfe, 0x3e}, rx: []byte{0, 0xe1, 1, 0xfe, 0x1e}}
	bad := testCardFrame(0, plain)
	bad[len(bad)-1] ^= 1
	usb := &cardUSB{t: t, replies: []cardReply{
		ifs, {tx: testCardFrame(0, apdu), rx: bad},
		ifs, {tx: testCardFrame(0, apdu), rx: testCardFrame(0, plain)},
	}}
	reader := usb.reader()
	if err := reader.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Transmit(apdu); err == nil {
		t.Fatal("不正なLRCを受け入れた")
	}
	if _, err := reader.Transmit(apdu); err == nil {
		t.Fatal("リセットせずに再送した")
	}
	if err := reader.Reset(); err != nil {
		t.Fatal(err)
	}
	got, err := reader.Transmit(apdu)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("リセット後の応答: %x %v", got, err)
	}
	if !slices.Equal(usb.outOffsets, []uint16{0x200, 0x200, 0x240, 0x200, 0x200, 0x240}) {
		t.Fatalf("送信の分割: %v", usb.outOffsets)
	}
}

func TestCardRejectsShortATRTransfer(t *testing.T) {
	usb := &cardUSB{t: t, shortRead: true}
	if err := usb.reader().Reset(); err == nil {
		t.Fatal("短いATR転送を受け入れた")
	}
}
