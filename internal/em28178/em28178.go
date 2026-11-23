// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

// Package em28178 は eMPIA EM28178（MonsterTV U3S の USB ブリッジ）を、ベンダーの制御転送で動かす。
// レジスタの読み書きと I2C だけを受け持つ（recfriio の Mtvu3s.cpp の Em28178I2cBus・emRegRead/Write から移した）。
package em28178

import (
	"fmt"

	"github.com/waya0125/recfriio/internal/usbfs"
)

// ベンダーの要求の番号
const (
	reqRegister = 0x00 // EM28178 のレジスタの読み書き
	reqI2CStop  = 0x02 // I2C の読み書き（最後に停止を送る）
	reqI2CCont  = 0x03 // I2C の読み書き（停止を送らない。repeated start）
)

const (
	requestTypeOut = 0x40 // ベンダー・機器あて・ホストから
	requestTypeIn  = 0xc0 // ベンダー・機器あて・機器から
	timeoutMS      = 1000
)

// EM28178 のレジスタ（名前は Linux の em28xx に合わせる）
const (
	RegI2CClock   = 0x06 // I2C のクロック
	RegUSBSuspend = 0x0c // サスペンド・電源
	RegTSConfig   = 0x50 // TS の入り口の形（地デジのとき 0x01）
	RegTSControl  = 0x5d // TS の出口の設定
	RegTSEnable   = 0x5f // TS1 の出口・同期・暗号の有効化
	RegKey        = 0x60 // 0x60..0x6f（16 バイト）
	RegGPIOShadow = 0x73 // GPIO の写し
	RegGPIO       = 0x80 // GPIO のポート 0（bit 1 = 電源で Low で入る、bit 7 = リセット、bit 0 = 復調部）
)

// Bridge は開いた EM28178。i2c.Bus としても使える。
type Bridge struct {
	usb *usbfs.Device
}

// New は usb の EM28178 を包む。
func New(usb *usbfs.Device) *Bridge { return &Bridge{usb: usb} }

// ReadReg はレジスタを 1 つ読む。
func (b *Bridge) ReadReg(reg uint8) (uint8, error) {
	var val [1]byte
	n, err := b.usb.Control(requestTypeIn, reqRegister, 0, uint16(reg), val[:], timeoutMS)
	if err != nil {
		return 0, fmt.Errorf("EM28178 のレジスタ %#02x を読めない: %w", reg, err)
	}
	if n != 1 {
		return 0, fmt.Errorf("EM28178 のレジスタ %#02x: %d バイトしか返らない", reg, n)
	}
	return val[0], nil
}

// WriteReg はレジスタを 1 つ書く。
func (b *Bridge) WriteReg(reg, val uint8) error {
	n, err := b.usb.Control(requestTypeOut, reqRegister, 0, uint16(reg), []byte{val}, timeoutMS)
	if err != nil {
		return fmt.Errorf("EM28178 のレジスタ %#02x に %#02x を書けない: %w", reg, val, err)
	}
	if n != 1 {
		return fmt.Errorf("EM28178 のレジスタ %#02x: %d バイトしか書けない", reg, n)
	}
	return nil
}

func i2cRequest(stop bool) uint8 {
	if stop {
		return reqI2CStop
	}
	return reqI2CCont
}

// Write は I2C の addr に data を書く（i2c.Bus）。
func (b *Bridge) Write(addr uint8, data []byte, stop bool) error {
	if len(data) > 256 {
		return fmt.Errorf("I2C %#02x への書き込みが長すぎる（%d バイト）", addr, len(data))
	}
	if _, err := b.usb.Control(requestTypeOut, i2cRequest(stop), 0, uint16(addr), data, timeoutMS); err != nil {
		return fmt.Errorf("I2C %#02x に書けない: %w", addr, err)
	}
	return nil
}

// Read は I2C の addr から len(data) バイト読む（i2c.Bus）。
func (b *Bridge) Read(addr uint8, data []byte, stop bool) error {
	n, err := b.usb.Control(requestTypeIn, i2cRequest(stop), 0, uint16(addr), data, timeoutMS)
	if err != nil {
		return fmt.Errorf("I2C %#02x から読めない: %w", addr, err)
	}
	if n != len(data) {
		return fmt.Errorf("I2C %#02x: %d バイトのうち %d バイトしか返らない", addr, len(data), n)
	}
	return nil
}
