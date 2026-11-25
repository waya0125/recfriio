// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

// Package mtvu3s は SKNET MonsterTV U3S（3275:9015）を動かす。
// EM28178 の USB ブリッジに、TC90522 が 2 つ（0x20 が地上波、0x22 が衛星）、
// その裏に FC0013（地上波）と QM1D1C0042（衛星）がつながっている。
// 手順は recfriio の Mtvu3s.cpp（2026-11-19 に地デジ・BS・CS で確かめたもの）から移した。
//
//	USB (EM28178)
//	 ├── I2C 0x20: TC90522 (ISDB-T) ──[0xfe]──▶ 0xc6: FC0013
//	 └── I2C 0x22: TC90522 (ISDB-S) ──[0xfe]──▶ 0xc0: QM1D1C0042
package mtvu3s

import (
	"errors"
	"fmt"
	"io"
	"syscall"
	"time"

	"github.com/waya0125/recfriio/internal/em28178"
	"github.com/waya0125/recfriio/internal/fc0013"
	"github.com/waya0125/recfriio/internal/i2c"
	"github.com/waya0125/recfriio/internal/qm1d1c0042"
	"github.com/waya0125/recfriio/internal/tc90522"
	"github.com/waya0125/recfriio/internal/usbfs"
)

// ID は MonsterTV U3S の USB の ID。
var ID = usbfs.ID{Vendor: 0x3275, Product: 0x9015}

// ErrNoDevice は使える MonsterTV U3S が無いこと。
var ErrNoDevice = errors.New("使える MonsterTV U3S（3275:9015）が見つからない")

// I2C のアドレス
const (
	terDemodAddr = 0x20
	satDemodAddr = 0x22
)

// USB の設定（recfriio の setting.hpp の MTVU3S_* と同じ）
const (
	usbInterface   = 0
	altIdle        = 0
	altStream      = 1 // isochronous の EP 0x84 が出てくる設定
	streamEndpoint = 0x84
	isoPacketSize  = 940 // 188 × 5
	isoPackets     = 16  // 1 URB = 16 マイクロフレーム = 2 ms
	// URB の数。recfriio は 32（64 ms ぶん）。Go の GC などで受け取りが遅れても落ちないよう多めにする
	urbCount = 128
	// 溜めておける Chunks の数（1 個は最大 15 KB。約 30 Mbps で 20 秒ぶん）
	queueChunks = 8192
)

// TS の出口の設定の値（Mtvu3s.cpp と同じ）
const (
	tsControlOn    = 0xef // 0x5d
	tsEnableStream = 0x89 // 0x5f: TS1 の出口・同期・暗号を有効に（公式の手順のまま）
	tsEnableIdle   = 0x08 // 0x5f: 止める
	terTSInput     = 0x01 // 0x50: 地デジのとき
)

// Device は開いた MonsterTV U3S。
type Device struct {
	USB    *usbfs.Device
	Bridge *em28178.Bridge

	Ter      *tc90522.Demod
	Sat      *tc90522.Demod
	TerTuner *fc0013.Tuner
	SatTuner *qm1d1c0042.Tuner

	// Log に選局の経過を書く
	Log io.Writer

	band      Band
	streaming bool
}

// OpenDevice は path の U3S を開いてインターフェースを取る。path が空なら、
// 見つかったもののうち、ほかのプロセスが使っていない最初のものを開く。
func OpenDevice(path string) (*Device, error) {
	paths := []string{path}
	if path == "" {
		found, err := usbfs.Find(ID)
		if err != nil {
			return nil, err
		}
		if len(found) == 0 {
			return nil, ErrNoDevice
		}
		paths = found
	}
	var last error
	for _, p := range paths {
		dev, err := open(p)
		if err == nil {
			return dev, nil
		}
		last = err
		if !errors.Is(err, syscall.EBUSY) {
			break
		}
	}
	return nil, last
}

func open(path string) (*Device, error) {
	id, err := usbfs.ReadID(path)
	if err != nil {
		return nil, err
	}
	if id != ID {
		return nil, fmt.Errorf("%s は %s（MonsterTV U3S は %s）", path, id, ID)
	}
	usb, err := usbfs.Open(path)
	if err != nil {
		return nil, err
	}
	if err := usb.Claim(usbInterface); err != nil {
		usb.Close()
		return nil, err
	}
	if err := usb.SetInterface(usbInterface, altIdle); err != nil {
		usb.Close()
		return nil, err
	}
	bridge := em28178.New(usb)
	ter := tc90522.New(bridge, terDemodAddr)
	sat := tc90522.New(bridge, satDemodAddr)
	return &Device{
		USB: usb, Bridge: bridge,
		Ter: ter, Sat: sat,
		TerTuner: fc0013.New(ter.Repeater(), fc0013.DefaultAddr),
		SatTuner: qm1d1c0042.New(sat.Repeater(), qm1d1c0042.DefaultAddr),
		Log:      io.Discard,
	}, nil
}

// Init は電源を入れ、復調部とチューナーを初期化する（Mtvu3s::UsbProcBegin）。
func (d *Device) Init() error {
	b := d.Bridge
	// EM28178 を起こし、TC90522 のリセットを解く（GPIO1 = Low で電源、GPIO7 = Low → High）
	steps := []struct {
		reg, val uint8
		wait     time.Duration
	}{
		{em28178.RegUSBSuspend, 0x10, 0},
		{em28178.RegI2CClock, 0x44, 0},
		{em28178.RegGPIO, 0x7c, 50 * time.Millisecond},
		{em28178.RegGPIO, 0xfd, 0},
		{em28178.RegGPIOShadow, 0xfd, 50 * time.Millisecond},
	}
	for _, s := range steps {
		if err := b.WriteReg(s.reg, s.val); err != nil {
			return err
		}
		time.Sleep(s.wait)
	}
	if err := d.Ter.InitTer(); err != nil {
		return err
	}
	if err := d.Sat.InitSat(); err != nil {
		return err
	}
	if err := d.SatTuner.Init(); err != nil {
		return err
	}
	if err := d.TerTuner.Init(); err != nil {
		return err
	}
	// TS の出口の設定（0x60..0x6f は公式の手順どおり 0 を書く）
	for i := range uint8(16) {
		if err := b.WriteReg(em28178.RegKey+i, 0x00); err != nil {
			return err
		}
	}
	return b.WriteReg(em28178.RegTSControl, tsControlOn)
}

// TuneResult は選局の結果。
type TuneResult struct {
	Locked bool
	// TSID は衛星で選んだ TS（地上波は 0）
	TSID uint16
}

// Tune は ch に合わせる（Mtvu3s::setChannel）。ロックしなくてもエラーにはしない（recfriio と同じ）。
func (d *Device) Tune(ch Channel) (TuneResult, error) {
	d.band = ch.Band
	if ch.Band == BandUHF {
		return d.tuneTer(ch.FreqKHz)
	}
	return d.tuneSat(ch)
}

func (d *Device) tuneTer(freqKHz uint32) (TuneResult, error) {
	if err := d.Bridge.WriteReg(em28178.RegTSConfig, terTSInput); err != nil {
		return TuneResult{}, err
	}
	if err := d.Sat.SetSatActive(false); err != nil {
		return TuneResult{}, err
	}
	if err := d.Ter.SetTerActive(true); err != nil {
		return TuneResult{}, err
	}
	if err := d.Ter.HoldAGCForTune(true); err != nil {
		return TuneResult{}, err
	}
	if err := d.TerTuner.Tune(freqKHz, fc0013.DefaultBWMHz); err != nil {
		return TuneResult{}, err
	}
	if err := d.Ter.HoldAGCForTune(false); err != nil {
		return TuneResult{}, err
	}
	time.Sleep(50 * time.Millisecond)
	if err := d.Ter.ResetTer(); err != nil {
		return TuneResult{}, err
	}
	locked, err := i2c.WaitUntil(func() (bool, error) {
		if err := d.TerTuner.AdjustLNA(); err != nil {
			return false, err
		}
		return d.Ter.TerLocked()
	}, tc90522.TerLockTries, tc90522.TerLockInterval)
	if err != nil {
		return TuneResult{}, err
	}
	fmt.Fprintf(d.Log, "ISDB-T %d kHz: locked=%v\n", freqKHz, locked)
	return TuneResult{Locked: locked}, nil
}

func (d *Device) tuneSat(ch Channel) (TuneResult, error) {
	freqKHz := ch.FreqKHz
	if err := d.Ter.SetTerActive(false); err != nil {
		return TuneResult{}, err
	}
	if err := d.Sat.SetSatActive(true); err != nil {
		return TuneResult{}, err
	}
	if err := d.SatTuner.Tune(freqKHz); err != nil {
		return TuneResult{}, err
	}
	if err := d.Sat.ResetSat(); err != nil {
		return TuneResult{}, err
	}
	tmcc, err := i2c.WaitUntil(d.Sat.SatTMCCLocked, tc90522.SatLockTries, tc90522.SatLockInterval)
	if err != nil {
		return TuneResult{}, err
	}
	// TMCC がつかめた直後は、TSID の一覧がまだ埋まっていない（0xfff8 だけのことがある）ので、
	// 空き以外が載るまで待つ
	var ids []uint16
	if _, err := i2c.WaitUntil(func() (bool, error) {
		var err error
		ids, err = d.Sat.SatTMCCTSIDs()
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			if !IsTMCCPlaceholder(id) {
				return true, nil
			}
		}
		return false, nil
	}, tc90522.SatLockTries, tc90522.SatLockInterval); err != nil {
		return TuneResult{}, err
	}
	want, ok := ch.PickTSID(ids)
	if ch.TSID == 0 && ch.Slot >= 0 {
		fmt.Fprintf(d.Log, "ISDB-S TMCC TSIDs: %#04x (want slot %d -> %#04x)\n", ids, ch.Slot, want)
	} else {
		fmt.Fprintf(d.Log, "ISDB-S TMCC TSIDs: %#04x (want %#04x -> %#04x)\n", ids, ch.TSID, want)
	}
	if !ok {
		// 無い TS を頼むと、復調部は黙って別の TS（一覧の最初など）を出すので、ここで止める
		var have []uint16
		for _, id := range ids {
			if !IsTMCCPlaceholder(id) {
				have = append(have, id)
			}
		}
		if ch.TSID == 0 && ch.Slot >= 0 {
			return TuneResult{}, fmt.Errorf("%s: このトランスポンダーの TS は %d 個で、%d 番目は無い（あるのは %#04x）", ch.Name, len(have), ch.Slot, have)
		}
		return TuneResult{}, fmt.Errorf("TSID %#04x の TS は、いまこのトランスポンダーに無い（あるのは %#04x）", ch.TSID, have)
	}
	selected, err := d.Sat.SelectSatTSID(want)
	if err != nil {
		return TuneResult{}, err
	}
	slot, err := i2c.WaitUntil(d.Sat.SatSlotLocked, tc90522.SatLockTries, tc90522.SatLockInterval)
	if err != nil {
		return TuneResult{}, err
	}
	fmt.Fprintf(d.Log, "ISDB-S %d kHz: tmcc=%v tsid=%#04x slot=%v\n", freqKHz, tmcc, selected, slot)
	return TuneResult{Locked: tmcc && slot, TSID: selected}, nil
}

// CNR は、いま選んでいる帯の C/N（dB）と生の値を返す。
func (d *Device) CNR() (float64, uint32, error) {
	if d.band == BandUHF {
		return d.Ter.TerCNR()
	}
	return d.Sat.SatCNR()
}

// StartStream は TS の出口を開けて、isochronous の受信を始める（Mtvu3s::startStream）。
func (d *Device) StartStream() (*usbfs.Stream, error) {
	if err := d.USB.SetInterface(usbInterface, altStream); err != nil {
		return nil, err
	}
	if err := d.Bridge.WriteReg(em28178.RegTSControl, tsControlOn); err != nil {
		return nil, err
	}
	if err := d.Bridge.WriteReg(em28178.RegTSEnable, tsEnableStream); err != nil {
		return nil, err
	}
	stream, err := d.USB.StartIsoStream(streamEndpoint, urbCount, isoPacketSize, isoPackets, queueChunks)
	if err != nil {
		return nil, err
	}
	d.streaming = true
	return stream, nil
}

// StopStream は受信を止めて TS の出口を閉じる（Mtvu3s::stopStream）。
func (d *Device) StopStream(stream *usbfs.Stream) error {
	if stream != nil {
		stream.Stop()
	}
	d.streaming = false
	return d.idle()
}

func (d *Device) idle() error {
	err := d.Bridge.WriteReg(em28178.RegTSEnable, tsEnableIdle)
	if serr := d.USB.SetInterface(usbInterface, altIdle); err == nil {
		err = serr
	}
	return err
}

// Close は TS の出口を閉じ（Mtvu3s::UsbProcEnd）、USB 機器を返す。
func (d *Device) Close() error {
	err := d.idle()
	if cerr := d.USB.Close(); err == nil {
		err = cerr
	}
	return err
}
