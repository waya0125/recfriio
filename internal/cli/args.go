// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

// Package cli は recfriio（と、互換の recut100b・recmtvu3s）のコマンドの中身。
// 引数を読み、機種を選んで開き、選局して TS を書き出す。
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/waya0125/recfriio/internal/mtvu3s"
	"github.com/waya0125/recfriio/internal/ut100b"
)

// Kind は機種。
type Kind int

const (
	Any    Kind = iota // 決めない（引数・機器・チャンネルで選ぶ）
	UT100B             // Quixun QRS-UT100B
	U3S                // SKNET MonsterTV U3S
)

func (k Kind) String() string {
	switch k {
	case UT100B:
		return "QRS-UT100B"
	case U3S:
		return "MonsterTV U3S"
	}
	return "any"
}

// channel は選局する先。機種によって受けられるものが違う。
type channel struct {
	text string
	// UT100B で受けるとき（地デジ・CATV）
	ut100bOK bool
	freqMHz  int
	// U3S で受けるとき（地デジ・BS・CS）
	u3sOK bool
	u3s   mtvu3s.Channel
}

// parseChannel はチャンネルの書き方を読む。
//
//	13〜62        地デジ（どちらの機種でも）
//	C13〜C63      CATV（K13 とも書ける。UT100B だけ）
//	BSnn_s・CSnn  BS・110 度 CS（U3S だけ）
func parseChannel(s string) (channel, error) {
	up := strings.ToUpper(s)
	ch := channel{text: s}
	switch {
	case strings.HasPrefix(up, "BS") || strings.HasPrefix(up, "CS"):
		c, err := mtvu3s.ParseChannel(s)
		if err != nil {
			return ch, err
		}
		ch.u3sOK, ch.u3s = true, c
		return ch, nil
	case strings.HasPrefix(up, "C") || strings.HasPrefix(up, "K"):
		n, err := strconv.Atoi(up[1:])
		if err != nil {
			return ch, fmt.Errorf("チャンネル %q が読めない", s)
		}
		f, err := ut100b.Frequency(n, true)
		if err != nil {
			return ch, err
		}
		ch.ut100bOK, ch.freqMHz = true, f
		return ch, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return ch, fmt.Errorf("チャンネル %q が読めない（13〜62、C13〜C63、BS15_0、CS2 など）", s)
	}
	f, err := ut100b.Frequency(n, false)
	if err != nil {
		return ch, err
	}
	c, err := mtvu3s.ParseChannel(s)
	if err != nil {
		return ch, err
	}
	ch.ut100bOK, ch.freqMHz = true, f
	ch.u3sOK, ch.u3s = true, c
	return ch, nil
}

// accepts は、その機種でこのチャンネルを受けられるか。
func (c channel) accepts(k Kind) bool {
	switch k {
	case UT100B:
		return c.ut100bOK
	case U3S:
		return c.u3sOK
	}
	return c.ut100bOK || c.u3sOK
}

type options struct {
	kind     Kind // Any なら選ぶ
	b25      bool
	pcsc     bool
	round    int
	strip    bool
	sync     bool
	stats    bool
	device   string
	raw      string
	cardLog  string
	firmware string // UT100B だけ
	// U3S だけ
	noSettle     bool
	noDescramble bool

	channel  channel
	seconds  int // 0 なら止められるまで
	output   string
	exitCode int
}

func usage(name string, forced Kind) string {
	channels := "13〜62（地デジ）、C13〜C63（CATV。UT100B だけ）、BS01_0〜BS23_7・CS2〜CS24（U3S だけ）"
	switch forced {
	case UT100B:
		channels = "13〜62（UHF）、C13〜C63（CATV。K13 とも書ける）"
	case U3S:
		channels = "13〜62（UHF）、BS01_0〜BS23_7（BS）、CS2〜CS24（110 度 CS）"
	}
	return fmt.Sprintf(`使い方: %s [オプション] チャンネル 秒 出力先
  チャンネル  %s
  秒          録る長さ。- なら止められるまで
  出力先      書き出すファイル。- なら標準出力

オプション:
`, name, channels)
}

func parseArgs(name string, forced Kind, args []string, stderr io.Writer) (*options, error) {
	o := &options{kind: forced}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage(name, forced))
		fs.PrintDefaults()
	}
	var hdp, u3s, mtvu3sFlag, hdus bool
	if forced == Any {
		fs.BoolVar(&hdp, "hdp", false, "QRS-UT100B を使う（C++ 版の recfriio と同じ書き方）")
		fs.BoolVar(&u3s, "u3s", false, "MonsterTV U3S を使う（C++ 版の recfriio と同じ書き方）")
		fs.BoolVar(&mtvu3sFlag, "mtvu3s", false, "--u3s と同じ")
		fs.BoolVar(&hdus, "hdus", false, "（Go 版では使えない。C++ 版はタグ cpp-final）")
	}
	fs.BoolVar(&o.b25, "b25", false, "チューナーの内蔵 B-CAS スロットでスクランブルを外す（ARIB STD-B25）")
	fs.BoolVar(&o.pcsc, "pcsc", false, "--b25 で PC/SC のカードリーダーを使う（--b25 も付いたことになる）")
	fs.IntVar(&o.round, "round", 4, "Multi2 の段数")
	fs.BoolVar(&o.strip, "strip", false, "ヌルパケットを捨てる")
	fs.BoolVar(&o.sync, "sync", false, "--b25 で、鍵がそろうまでの失敗を待つ")
	fs.BoolVar(&o.stats, "stats", false, "終わりに TS のヘッダーを数えた結果（同期・TEI・連続カウンター・PID・PAT の TSID）を書く")
	fs.StringVar(&o.device, "device", "", "使う機器ファイル（/dev/bus/usb/BBB/DDD）。空なら空いているもの")
	fs.StringVar(&o.raw, "raw", "", "B25 を通す前の TS も書き出す（照合用）")
	fs.StringVar(&o.cardLog, "card-log", "", "B-CAS カードとのやり取りを書き出す（照合用。鍵が入るので人に渡さない）")
	if forced != U3S {
		fs.StringVar(&o.firmware, "firmware", "", "（UT100B）ファームの無い UT100B（1738:5211）しか無ければ、このファームを送ってから使う")
	}
	if forced != UT100B {
		fs.BoolVar(&o.noSettle, "no-settle", false, "（U3S）受け始めの、復調部が落ち着く前のパケット（TEI 付き）も捨てずに書き出す")
		fs.BoolVar(&o.noDescramble, "no-descramble", false, "（U3S）EM28178 の USB 転送の暗号を外さずに書き出す（照合用）")
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if hdus {
		return nil, errors.New("--hdus（MonsterTV HDUS）は Go 版では使えない（C++ 版はタグ cpp-final にある）")
	}
	u3s = u3s || mtvu3sFlag
	switch {
	case hdp && u3s:
		return nil, errors.New("--hdp と --u3s は一緒に使えない")
	case hdp:
		o.kind = UT100B
	case u3s:
		o.kind = U3S
	}
	if fs.NArg() != 3 {
		fs.Usage()
		return nil, errors.New("チャンネル・秒・出力先の 3 つを渡す")
	}
	ch, err := parseChannel(fs.Arg(0))
	if err != nil {
		return nil, err
	}
	if !ch.accepts(o.kind) {
		return nil, fmt.Errorf("%s では チャンネル %s を受けられない", o.kind, fs.Arg(0))
	}
	o.channel = ch
	if fs.Arg(1) != "-" {
		if o.seconds, err = strconv.Atoi(fs.Arg(1)); err != nil || o.seconds <= 0 {
			return nil, fmt.Errorf("秒 %q が読めない", fs.Arg(1))
		}
	}
	o.output = fs.Arg(2)
	if o.pcsc {
		o.b25 = true
	}
	return o, nil
}
