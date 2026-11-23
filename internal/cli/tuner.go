// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/waya0125/recfriio/internal/asie5606"
	"github.com/waya0125/recfriio/internal/bcas"
	"github.com/waya0125/recfriio/internal/firmware"
	"github.com/waya0125/recfriio/internal/mtvu3s"
	"github.com/waya0125/recfriio/internal/usbfs"
	"github.com/waya0125/recfriio/internal/ut100b"
)

// tuner は、機種ごとの違いをまとめた口。
type tuner interface {
	Kind() Kind
	Path() string
	// Tune は初期化して選局する
	Tune(ch channel, log io.Writer) error
	CNR() (float64, uint32, error)
	StartStream() (*usbfs.Stream, error)
	StopStream(*usbfs.Stream) error
	// NewFeeder は USB から届いたものを TS のパケットにする口を作る
	NewFeeder(o *options, log io.Writer) feeder
	// BuiltinCard は内蔵の B-CAS スロット（無ければ nil）
	BuiltinCard(log io.Writer) bcas.Transport
	Close() error
}

// feeder は USB から届いたバイト列を受け、書き出してよい TS のパケットを返す。
type feeder interface {
	Feed(chunk []byte) []byte
	// Report は終わりに、つかみ直しなどの数を書く
	Report(log io.Writer)
}

// ErrNoDevice は使える機器が無いこと。
var ErrNoDevice = errors.New("使える機器が見つからない（QRS-UT100B 3275:7020、MonsterTV U3S 3275:9015）")

// openTuner は、引数・機器ファイル・チャンネルから機種を決めて開く。
// --device が無ければ、受けられる機種を UT100B → U3S の順に探し、ほかのプロセスが使っていないものを開く。
func openTuner(o *options, log io.Writer) (tuner, error) {
	if o.device != "" {
		id, err := usbfs.ReadID(o.device)
		if err != nil {
			return nil, err
		}
		var kind Kind
		switch id {
		case ut100b.WarmID:
			kind = UT100B
		case mtvu3s.ID:
			kind = U3S
		default:
			return nil, fmt.Errorf("%s は %s（使えるのは QRS-UT100B %s と MonsterTV U3S %s）", o.device, id, ut100b.WarmID, mtvu3s.ID)
		}
		if o.kind != Any && o.kind != kind {
			return nil, fmt.Errorf("%s は %s で、%s ではない", o.device, kind, o.kind)
		}
		if !o.channel.accepts(kind) {
			return nil, fmt.Errorf("%s（%s）では チャンネル %s を受けられない", o.device, kind, o.channel.text)
		}
		return openKind(kind, o, log)
	}
	var last error
	for _, kind := range []Kind{UT100B, U3S} {
		if (o.kind != Any && o.kind != kind) || !o.channel.accepts(kind) {
			continue
		}
		t, err := openKind(kind, o, log)
		if err == nil {
			return t, nil
		}
		if !errors.Is(err, ut100b.ErrNoDevice) && !errors.Is(err, mtvu3s.ErrNoDevice) && !errors.Is(err, syscall.EBUSY) {
			return nil, err
		}
		if last == nil || errors.Is(err, syscall.EBUSY) {
			last = err
		}
	}
	if last == nil || errors.Is(last, ut100b.ErrNoDevice) || errors.Is(last, mtvu3s.ErrNoDevice) {
		if o.kind != Any {
			return nil, fmt.Errorf("使える %s が見つからない", o.kind)
		}
		return nil, ErrNoDevice
	}
	return nil, last
}

func openKind(kind Kind, o *options, log io.Writer) (tuner, error) {
	switch kind {
	case UT100B:
		dev, err := ut100b.OpenDevice(o.device)
		if errors.Is(err, ut100b.ErrNoDevice) && o.firmware != "" && o.device == "" {
			if lerr := loadFirmware(o.firmware, log); lerr != nil {
				return nil, lerr
			}
			dev, err = ut100b.OpenDevice("")
		}
		if err != nil {
			return nil, err
		}
		return &ut100bTuner{dev: dev}, nil
	case U3S:
		dev, err := mtvu3s.OpenDevice(o.device)
		if err != nil {
			return nil, err
		}
		return &u3sTuner{dev: dev}, nil
	}
	return nil, fmt.Errorf("知らない機種 %v", kind)
}

func loadFirmware(path string, log io.Writer) error {
	image, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	index, ok := firmware.IndexFor(image)
	if !ok {
		return fmt.Errorf("%s は知らないファーム（ut100b-firmware load --index で送る）", path)
	}
	fmt.Fprintf(log, "loading the firmware %s...\n", path)
	dev, err := firmware.Load(image, index, ut100b.ColdID, ut100b.WarmID, 5*time.Second)
	if err != nil {
		return err
	}
	fmt.Fprintf(log, "the firmware is running: %s\n", dev)
	return nil
}

// ---- QRS-UT100B

type ut100bTuner struct{ dev *ut100b.Device }

func (t *ut100bTuner) Kind() Kind   { return UT100B }
func (t *ut100bTuner) Path() string { return t.dev.USB.Path() }

func (t *ut100bTuner) Tune(ch channel, log io.Writer) error {
	t.dev.Tuner.Log = log
	if err := t.dev.Tuner.Open(); err != nil {
		return err
	}
	return t.dev.Tuner.Tune(ch.freqMHz)
}

func (t *ut100bTuner) CNR() (float64, uint32, error)       { return t.dev.Tuner.CNR() }
func (t *ut100bTuner) StartStream() (*usbfs.Stream, error) { return t.dev.StartStream() }

func (t *ut100bTuner) StopStream(s *usbfs.Stream) error {
	s.Stop()
	return nil
}

func (t *ut100bTuner) NewFeeder(_ *options, log io.Writer) feeder {
	a := ut100b.NewAligner()
	a.Log = log
	return ut100bFeeder{a}
}

func (t *ut100bTuner) BuiltinCard(log io.Writer) bcas.Transport {
	reader := asie5606.NewReader(t.dev.Bridge, t.dev.Tuner.CardAddr)
	reader.Log = log
	return reader
}

func (t *ut100bTuner) Close() error { return t.dev.Close() }

type ut100bFeeder struct{ *ut100b.Aligner }

func (f ut100bFeeder) Report(log io.Writer) {
	if f.Resyncs > 0 {
		fmt.Fprintf(log, "TS: %d resyncs\n", f.Resyncs)
	}
}

// ---- MonsterTV U3S

type u3sTuner struct{ dev *mtvu3s.Device }

func (t *u3sTuner) Kind() Kind   { return U3S }
func (t *u3sTuner) Path() string { return t.dev.USB.Path() }

func (t *u3sTuner) Tune(ch channel, log io.Writer) error {
	t.dev.Log = log
	if err := t.dev.Init(); err != nil {
		return err
	}
	c := ch.u3s
	fmt.Fprintf(log, "channel: %s (%s, %d kHz)\n", c.Name, c.Band, c.FreqKHz)
	_, err := t.dev.Tune(c)
	return err
}

func (t *u3sTuner) CNR() (float64, uint32, error)       { return t.dev.CNR() }
func (t *u3sTuner) StartStream() (*usbfs.Stream, error) { return t.dev.StartStream() }
func (t *u3sTuner) StopStream(s *usbfs.Stream) error    { return t.dev.StopStream(s) }
func (t *u3sTuner) BuiltinCard(io.Writer) bcas.Transport {
	return t.dev.CardReader()
}
func (t *u3sTuner) Close() error { return t.dev.Close() }

func (t *u3sTuner) NewFeeder(o *options, log io.Writer) feeder {
	f := &u3sFeeder{aligner: mtvu3s.NewAligner()}
	f.aligner.Log = log
	if !o.noDescramble {
		d, err := mtvu3s.NewDescrambler([16]byte{})
		if err != nil {
			// 鍵の長さは決まっているので起きない
			panic(err)
		}
		f.aligner.Descrambler = d
	}
	if !o.noSettle {
		f.settler = mtvu3s.NewSettler()
	}
	return f
}

type u3sFeeder struct {
	aligner *mtvu3s.Aligner
	settler *mtvu3s.Settler
}

func (f *u3sFeeder) Feed(chunk []byte) []byte {
	packets := f.aligner.Feed(chunk)
	if f.settler != nil {
		packets = f.settler.Feed(packets)
	}
	return packets
}

func (f *u3sFeeder) Report(log io.Writer) {
	if s := f.settler; s != nil && (s.Dropped > 0 || !s.Settled() || s.GaveUp) {
		fmt.Fprintf(log, "settle: dropped %d packets at the start (settled=%v, gave up=%v)\n", s.Dropped, s.Settled(), s.GaveUp)
	}
	if a := f.aligner; a.Resyncs > 0 || a.BadSync > 0 {
		fmt.Fprintf(log, "TS: %d resyncs, %d packets without 0x47, %d bytes skipped\n", a.Resyncs, a.BadSync, a.Skipped)
	}
}
