// SPDX-License-Identifier: MIT

package mtvu3s

import (
	"bytes"
	"crypto/aes"
	"errors"
	"fmt"
	"time"

	"github.com/waya0125/recfriio/internal/bcas"
	"github.com/waya0125/recfriio/internal/em28178"
)

const (
	cardStatus = 0x70
	cardLength = 0x71
	cardLenHi  = 0x72
	cardMode   = 0x75

	cardBufferRequest = 0x14
	cardBufferIn      = 0xc0
	cardBufferOut     = 0x40
	cardBufferSend    = 0x0200
	cardReadyBits     = 0x05
	cardMaxAPDU       = 124
	cardMaxReply      = 254
)

var cardATR = [...]byte{0x3b, 0xf0, 0x12, 0, 0xff, 0x91, 0x81, 0xb1, 0x7c, 0x45, 0x1f, 3, 0x99}

// CardReader は、録画用に開いた U3S の内蔵カードスロットを使う。
type CardReader struct {
	readReg  func(uint8) (uint8, error)
	writeReg func(uint8, uint8) error
	control  func(uint8, uint8, uint16, uint16, []byte, uint32) (int, error)
	sleep    func(time.Duration)
	ready    bool
	ifsdSet  bool
	nextPCB  byte
}

// CardReader は USB 接続を借りる。リセットは bcas.Transport の利用開始時に行う。
func (d *Device) CardReader() *CardReader {
	return &CardReader{
		readReg: d.Bridge.ReadReg, writeReg: d.Bridge.WriteReg,
		control: d.USB.Control, sleep: time.Sleep,
	}
}

func (r *CardReader) buffer(direction uint8, start uint16, data []byte) error {
	for len(data) != 0 {
		chunk := data[:min(len(data), 64)]
		n, err := r.control(direction, cardBufferRequest, 0, start, chunk, 1000)
		if err != nil {
			return fmt.Errorf("内蔵カードのUSBバッファ %#04x: %w", start, err)
		}
		if n != len(chunk) {
			return fmt.Errorf("内蔵カードのUSBバッファ %#04x: %d/%d バイト転送", start, n, len(chunk))
		}
		start += uint16(n)
		data = data[n:]
	}
	return nil
}

func (r *CardReader) Reset() error {
	r.ready, r.ifsdSet, r.nextPCB = false, false, 0
	if err := r.writeReg(em28178.RegGPIOShadow, 0); err != nil {
		return err
	}
	r.sleep(700 * time.Millisecond)
	status, err := r.readReg(cardStatus)
	if err != nil {
		return err
	}
	if status&cardReadyBits != cardReadyBits {
		return errors.New("内蔵カードのATRが届かない")
	}
	length, err := r.readReg(cardLength)
	if err != nil {
		return err
	}
	// shortcut: 実機で確認した T=1/LRC のATRに限定し、別のATRを扱うときにパラメーターを解析する。
	if int(length) != len(cardATR) {
		return fmt.Errorf("内蔵カードのATRの長さ %d は未対応", length)
	}
	var atr [len(cardATR)]byte
	if err := r.buffer(cardBufferIn, 0, atr[:]); err != nil {
		return err
	}
	if atr != cardATR {
		return errors.New("内蔵カードのATRは未対応")
	}
	r.ready = true
	return nil
}

func (r *CardReader) Transmit(apdu []byte) ([]byte, error) {
	// shortcut: IFSC 124 の単一Iブロックに限定し、長いAPDUが必要になったらチェーンを実装する。
	if len(apdu) < 4 || len(apdu) > cardMaxAPDU {
		return nil, fmt.Errorf("%w: 内蔵カードのAPDU長 %d（4〜%d バイト）", bcas.ErrAPDULength, len(apdu), cardMaxAPDU)
	}
	if !r.ready {
		return nil, errors.New("内蔵カードのリセットが必要")
	}
	r.ready = false
	if !r.ifsdSet {
		ifsd := []byte{cardMaxReply}
		reply, err := r.exchange(0xc1, 0xe1, ifsd)
		if err != nil {
			return nil, fmt.Errorf("内蔵カードのIFS交渉: %w", err)
		}
		if !bytes.Equal(reply, ifsd) {
			return nil, errors.New("内蔵カードがIFSを受け入れなかった")
		}
		r.ifsdSet = true
	}
	reply, err := r.exchange(r.nextPCB, r.nextPCB, apdu)
	if err != nil {
		return nil, err
	}
	if len(reply) < 2 {
		return nil, errors.New("内蔵カードのAPDU応答が短い")
	}
	r.nextPCB ^= 0x40
	r.ready = true
	return reply, nil
}

func cardLRC(data []byte) (lrc byte) {
	for _, b := range data {
		lrc ^= b
	}
	return lrc
}

func (r *CardReader) exchange(pcb, expectedPCB byte, payload []byte) ([]byte, error) {
	tx := make([]byte, len(payload)+4)
	tx[1], tx[2] = pcb, byte(len(payload))
	copy(tx[3:], payload)
	tx[len(tx)-1] = cardLRC(tx)
	if err := r.buffer(cardBufferOut, cardBufferSend, tx); err != nil {
		return nil, err
	}
	mode, err := r.readReg(cardMode)
	if err != nil {
		return nil, err
	}
	if err := r.writeReg(cardMode, (mode&0xfb)|1); err != nil {
		return nil, err
	}
	if err := r.writeReg(cardStatus, 0xff); err != nil {
		return nil, err
	}
	if err := r.writeReg(cardLength, byte(len(tx))); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(2 * time.Second)
	// 応答長を早すぎる時点で読むと失敗するため、試験DLLと同じ時間だけ待つ。
	r.sleep(80 * time.Millisecond)
	var rx [cardMaxReply + 4]byte
	for ; time.Now().Before(deadline); r.sleep(5 * time.Millisecond) {
		status, err := r.readReg(cardStatus)
		if err != nil {
			return nil, err
		}
		if status&cardReadyBits != cardReadyBits {
			continue
		}
		lo, err := r.readReg(cardLength)
		if err != nil {
			return nil, err
		}
		hi, err := r.readReg(cardLenHi)
		if err != nil {
			return nil, err
		}
		count := int(lo) | int(hi)<<8
		if count < 4 {
			continue
		}
		if count > len(rx) {
			return nil, fmt.Errorf("内蔵カードの受信長が不正: %d", count)
		}
		frame := rx[:count]
		if err := r.buffer(cardBufferIn, 0, frame); err != nil {
			return nil, err
		}
		size := int(frame[2])
		// 受信完了ビットはT=1ブロック全体が届く前にも立つ。
		if count < size+4 {
			continue
		}
		if count != size+4 || frame[0] != 0 || frame[1] != expectedPCB {
			return nil, errors.New("内蔵カードのT=1ヘッダーが不正")
		}
		if err := restoreCardReply(payload, frame); err != nil {
			return nil, err
		}
		return frame[3 : 3+size], nil
	}
	return nil, errors.New("内蔵カードの応答がタイムアウト")
}

func restoreCardReply(apdu, frame []byte) error {
	// shortcut: recfriio が使う初期設定とECMの実機確認済み形式だけを復元する。
	// どちらも暗号化ブロック全体が受信長に収まる。
	initial := len(frame) == 65 && len(apdu) >= 5 && apdu[1] == 0x30
	ecm := len(frame) == 29 && len(apdu) >= 5 && apdu[1] == 0x34
	if (initial || ecm) && apdu[0] == 0x90 && apdu[2] == 0 && apdu[3] == 0 &&
		frame[3] == 0 && int(frame[4]) == len(frame)-8 {
		var zeroKey [16]byte
		cipher, err := aes.NewCipher(zeroKey[:])
		if err != nil {
			return err
		}
		block := frame[9 : 9+aes.BlockSize]
		cipher.Decrypt(block, block)
		if frame[len(frame)-3] != 0x90 || frame[len(frame)-2] != 0 ||
			(initial && (frame[7] != 0x21 || frame[8] != 0 || frame[9] != 0 || frame[10] != 5)) {
			return errors.New("内蔵カードの復元後のB-CAS応答が不正")
		}
	}
	if cardLRC(frame) != 0 {
		return errors.New("内蔵カードのT=1 LRCが一致しない")
	}
	return nil
}
