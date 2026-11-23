// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

package cli

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/waya0125/recfriio/internal/b25"
	"github.com/waya0125/recfriio/internal/bcas"
	"github.com/waya0125/recfriio/internal/pcsc"
	"github.com/waya0125/recfriio/internal/tscheck"
)

// 受け始めの C/N がこれより低ければ読み直す（C++ 版の recfriio と同じ）。
const (
	signalLevelThreshold = 10.0
	signalLevelRetries   = 2
	signalLevelInterval  = 400 * time.Millisecond
)

// b25ErrorMax 回失敗したら、B25 をやめて録り続ける（C++ 版の recfriio と同じ）。
const b25ErrorMax = 100

// Main はコマンドの入り口。name は使い方に出す名前、forced は機種を決めておくとき（Any なら選ぶ）。
// 終わりのコードを返す。
func Main(name string, forced Kind, args []string) int {
	o, err := parseArgs(name, forced, args, os.Stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
		}
		return 1
	}
	if err := run(o, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return o.exitCode
}

func run(o *options, log io.Writer) error {
	t, err := openTuner(o, log)
	if err != nil {
		return err
	}
	defer t.Close()
	fmt.Fprintf(log, "device: %s (%s)\n", t.Path(), t.Kind())

	if err := t.Tune(o.channel, log); err != nil {
		return err
	}
	for range signalLevelRetries {
		cnr, raw, err := t.CNR()
		if err != nil {
			return err
		}
		fmt.Fprintf(log, "CNR: %.4f dB (raw %#x)\n", cnr, raw)
		if cnr >= signalLevelThreshold {
			break
		}
		time.Sleep(signalLevelInterval)
	}

	var decoder *b25Decoder
	if o.b25 {
		decoder, err = newDecoder(o, t, log)
		if err != nil {
			// C++ 版と同じく、B25 なしで続け、終わりのコードを 1 にする
			fmt.Fprintln(log, err)
			fmt.Fprintln(log, "disable b25 decoding.")
			decoder = nil
			o.exitCode = 1
		} else {
			defer decoder.Close()
		}
	}

	var out io.Writer = os.Stdout
	if o.output != "-" {
		file, err := os.Create(o.output)
		if err != nil {
			return err
		}
		defer file.Close()
		out = file
	}
	var raw *os.File
	if o.raw != "" {
		if raw, err = os.Create(o.raw); err != nil {
			return err
		}
		defer raw.Close()
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE)
	defer signal.Stop(signals)

	fmt.Fprintln(log, "Output ts file.")
	start := time.Now()
	var deadline <-chan time.Time
	if o.seconds > 0 {
		timer := time.NewTimer(time.Duration(o.seconds) * time.Second)
		defer timer.Stop()
		deadline = timer.C
	}

	stream, err := t.StartStream()
	if err != nil {
		return err
	}
	feed := t.NewFeeder(o, log)
	var stats *tscheck.Stats
	if o.stats {
		stats = tscheck.New()
	}
	b25Errors := 0
	synced := false
	write := func(data []byte) error {
		if len(data) == 0 {
			return nil
		}
		_, err := out.Write(data)
		return err
	}

	var writeErr error
loop:
	for {
		select {
		case <-deadline:
			break loop
		case s := <-signals:
			fmt.Fprintf(log, "interrupted (%v).\n", s)
			break loop
		case chunk, ok := <-stream.Chunks:
			if !ok {
				fmt.Fprintln(log, "the USB stream ended.")
				break loop
			}
			packets := feed.Feed(chunk)
			if len(packets) == 0 {
				// 受け始めの捨てている間など。空のまま B25 に渡すと「TS ではない入力」で失敗を数えてしまう
				continue
			}
			if stats != nil {
				stats.Add(packets)
			}
			if raw != nil {
				if _, err := raw.Write(packets); err != nil {
					writeErr = err
					break loop
				}
			}
			if decoder != nil {
				decoded, err := decoder.Put(packets)
				if err != nil {
					if errors.Is(err, bcas.ErrAPDULength) {
						fmt.Fprintf(log, "B25 Error: %v\n", err)
						o.exitCode = 1
						writeErr = write(decoded)
						decoder = nil
						break loop
					}
					if !synced && o.sync {
						fmt.Fprintln(log, "Wait for B25 sync")
						continue
					}
					b25Errors++
					if b25Errors == b25ErrorMax {
						fmt.Fprintf(log, "B25 Error: %v\n", err)
						fmt.Fprintln(log, "Continue recording without B25.")
						decoder.Close()
						decoder = nil
						o.exitCode = 1
					}
					// U3Sの内蔵カードでECMが失敗しても、消費済みのTSは書き出す。
					if o.pcsc || t.Kind() != U3S || !errors.Is(err, b25.ErrECMProcess) {
						continue
					}
				}
				if len(decoded) > 0 {
					synced = true
				}
				packets = decoded
			}
			if err := write(packets); err != nil {
				writeErr = err
				break loop
			}
		}
	}
	if err := t.StopStream(stream); err != nil {
		fmt.Fprintf(log, "stop: %v\n", err)
	}
	elapsed := time.Since(start)
	if stream.Dropped > 0 || stream.Errors > 0 || stream.FrameErrors > 0 {
		fmt.Fprintf(log, "USB: dropped %d chunks, %d failed transfers, %d failed frames (last: %v)\n",
			stream.Dropped, stream.Errors, stream.FrameErrors, stream.LastError)
	}
	feed.Report(log)
	if decoder != nil && writeErr == nil {
		rest, err := decoder.Flush()
		if err != nil {
			fmt.Fprintf(log, "B25 Error: %v\n", err)
			o.exitCode = 1
		}
		writeErr = write(rest)
	}
	if writeErr != nil {
		fmt.Fprintf(log, "write failed: %v\n", writeErr)
		o.exitCode = 1
	}
	fmt.Fprintln(log, "done.")
	fmt.Fprintf(log, "Rec time: %.6f sec.\n", elapsed.Seconds())
	if cnr, raw, err := t.CNR(); err == nil {
		fmt.Fprintf(log, "CNR: %.4f dB (raw %#x)\n", cnr, raw)
	}
	if stats != nil {
		stats.Print(log, 8)
	}
	return nil
}

// b25Decoder は B25 の復号器と、閉じるものの組。
type b25Decoder struct {
	*b25.Decoder
	closers []io.Closer
}

func (d *b25Decoder) Close() {
	for _, c := range d.closers {
		c.Close()
	}
	d.closers = nil
}

// newDecoder は B25 の復号器を作る。--pcsc を指定すると外付けリーダーを優先する。
func newDecoder(o *options, t tuner, log io.Writer) (*b25Decoder, error) {
	d := &b25Decoder{}
	var transport bcas.Transport
	if !o.pcsc {
		transport = t.BuiltinCard(log)
	}
	useU3SBuiltin := transport != nil && t.Kind() == U3S
	if transport == nil {
		reader, err := pcsc.Open()
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(log, "B-CAS: PC/SC reader %q (%s)\n", reader.Name, pcsc.Socket())
		d.closers = append(d.closers, reader)
		transport = reader
	} else {
		fmt.Fprintln(log, "B-CAS: built-in slot of the tuner")
	}
	if o.cardLog != "" {
		file, err := os.OpenFile(o.cardLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			d.Close()
			return nil, err
		}
		d.closers = append(d.closers, file)
		transport = &loggingTransport{t: transport, w: file}
	}
	var card *bcas.Card
	var err error
	if useU3SBuiltin {
		// カードの待機中は録画側がキューを読めない。連続リセットによる欠落を抑える。
		card, err = bcas.OpenWithECMRetries(transport, 1)
	} else {
		card, err = bcas.Open(transport)
	}
	if err != nil {
		d.Close()
		return nil, err
	}
	d.Decoder = b25.New(card, o.round, o.strip)
	fmt.Fprintln(log, "B25Decoder initialized.")
	return d, nil
}

// loggingTransport はカードとのやり取りを 1 行ずつ書く（"> 送ったもの" と "< 答え"）。
// 照合の道具（tools/arib25-compare）が読み戻す。
type loggingTransport struct {
	t bcas.Transport
	w io.Writer
}

func (l *loggingTransport) Reset() error {
	fmt.Fprintln(l.w, "reset")
	return l.t.Reset()
}

func (l *loggingTransport) Transmit(apdu []byte) ([]byte, error) {
	r, err := l.t.Transmit(apdu)
	if err != nil {
		fmt.Fprintf(l.w, "> %s\n! %v\n", hex.EncodeToString(apdu), err)
		return r, err
	}
	fmt.Fprintf(l.w, "> %s\n< %s\n", hex.EncodeToString(apdu), hex.EncodeToString(r))
	return r, nil
}
