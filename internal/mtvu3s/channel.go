// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

package mtvu3s

import (
	"fmt"
	"strconv"
	"strings"
)

// Band は放送の帯。
type Band int

const (
	BandUHF Band = iota // 地上波
	BandBS
	BandCS // 110 度 CS
)

func (b Band) String() string {
	switch b {
	case BandUHF:
		return "UHF"
	case BandBS:
		return "BS"
	case BandCS:
		return "CS"
	}
	return fmt.Sprintf("Band(%d)", int(b))
}

// チャンネルから周波数を出す定数（kHz。BS・CS は第一 IF）
const (
	uhfBaseKHz = 473143 // ch13 の中心
	uhfStepKHz = 6000
	bsBaseKHz  = 1049480 // BS1
	bsStepKHz  = 38360
	csBaseKHz  = 1613000 // ND2
	csStepKHz  = 40000
)

// csTSIDs は 110 度 CS の各トランスポンダーの TSID。
// 上の 4 ビットはネットワーク（0x6 と 0x7）で、CS8・CS10 は 0x6 の側にある（2026-11-20 にわやさんの指摘で直した。元の表は 0x7080・0x70a0）。
// recfriio は CS で TSID を渡さず TMCC の最初のものを選んでいて、CS2 では 0xfff8 を選んでいた。
var csTSIDs = map[int]uint16{
	2: 0x6020, 4: 0x7040, 6: 0x7060, 8: 0x6080, 10: 0x60a0, 12: 0x70c0,
	14: 0x70e0, 16: 0x7100, 18: 0x7120, 20: 0x7140, 22: 0x7160, 24: 0x7180,
}

// Channel は選局する先。
type Channel struct {
	Name    string
	Band    Band
	FreqKHz uint32
	// TSID は衛星で選ぶ TS（--tsid で直接渡したときと CS）。
	// 0 なら、BS は Slot で選び、それ以外は TMCC にある最初のもの。
	// 選ぶときに TMCC の一覧と突き合わせる（MatchTSID）。
	TSID uint16
	// Slot は BS の「BSnn_s」の s。そのトランスポンダーの何番目の TS か
	// （TMCC の一覧から空きを除いた並びで数える。recpt1・recisdb・Mirakurun の channels.yml と同じ読み方）。
	// BS 以外は -1。
	Slot int
}

// PickTSID は、TMCC の TSID の一覧 ids から、このチャンネルで選ぶ TSID を決める。
// TSID が決まっていればそれを MatchTSID で当て、BS で TSID が 0 なら Slot 番目（空きを除く）を選ぶ。
// 選べなければ ok が false で、id は頼んだもの（Slot で選ぶときは 0）。
func (c Channel) PickTSID(ids []uint16) (id uint16, ok bool) {
	if c.TSID != 0 || c.Band != BandBS || c.Slot < 0 {
		return MatchTSID(c.TSID, ids)
	}
	n := 0
	for _, id := range ids {
		if IsTMCCPlaceholder(id) {
			continue
		}
		if n == c.Slot {
			return id, true
		}
		n++
	}
	return 0, false
}

// bsYearMask は BS の TSID のうち、登録年のビット。
const bsYearMask = 0x0e00

// csNetworkMask は CS の TSID のうち、ネットワーク（0x6 と 0x7）を分けるビット。
// トランスポンダーがネットワークを移っても選べるように、表と合わなければここを無視して当てる。
const csNetworkMask = 0x1000

// MatchTSID は、want に当たる TSID を TMCC の一覧 ids から選ぶ。
// 同じものがあればそれ、BS なら登録年のビット、CS ならネットワークのビットを無視して同じものを返す。
// どれも無ければ ok が false。
// want が 0 なら（TS を選ばない）、0 と true。
func MatchTSID(want uint16, ids []uint16) (id uint16, ok bool) {
	if want == 0 {
		return 0, true
	}
	for _, id := range ids {
		if id == want {
			return id, true
		}
	}
	if want&0xf000 == 0x4000 {
		for _, id := range ids {
			if id&0xf000 == 0x4000 && id&^bsYearMask == want&^bsYearMask {
				return id, true
			}
		}
	}
	if want&0xe000 == 0x6000 {
		for _, id := range ids {
			if id&0xe000 == 0x6000 && id&^csNetworkMask == want&^csNetworkMask {
				return id, true
			}
		}
	}
	return want, false
}

// IsTMCCPlaceholder は、TMCC の TSID の一覧で空きを表す値か（実機では 0xfff8・0xfff9 が見える）。
func IsTMCCPlaceholder(id uint16) bool { return id >= 0xfff0 }

// ParseChannel はチャンネルの書き方を読む（recfriio の --u3s と同じ）。
//
//	13〜62    UHF
//	BSnn_s    BS（nn は 1〜23 の奇数、s は 0〜7。s はトランスポンダーの何番目の TS か）
//	CSnn      110 度 CS（nn は 2〜24 の偶数）
func ParseChannel(s string) (Channel, error) {
	up := strings.ToUpper(s)
	switch {
	case strings.HasPrefix(up, "BS"):
		node, slot, ok := strings.Cut(up[2:], "_")
		n, err := strconv.Atoi(node)
		if !ok || err != nil || n < 1 || n > 23 || n%2 == 0 {
			return Channel{}, fmt.Errorf("BS のチャンネル %q: BS の番号は 1〜23 の奇数（例: BS15_0）", s)
		}
		sl, err := strconv.Atoi(slot)
		if err != nil || len(slot) != 1 || sl < 0 || sl > 7 {
			return Channel{}, fmt.Errorf("BS のチャンネル %q: スロットは 0〜7（例: BS15_0）", s)
		}
		// C++ 版の recfriio は s を TSID の下位 4 ビットとして 0x4000 | nn<<4 | s を頼んでいた
		// （BS15 だけ 1 つずらす）。再編で TS の番号が飛んだトランスポンダー（BS09・BS23 など）で
		// channels.yml と食い違うので、s は TMCC の一覧の並び（Slot）として選ぶ（issue #4）。
		return Channel{
			Name:    fmt.Sprintf("BS%02d_%s", n, slot),
			Band:    BandBS,
			FreqKHz: bsBaseKHz + uint32((n-1)/2)*bsStepKHz,
			Slot:    sl,
		}, nil
	case strings.HasPrefix(up, "CS"):
		n, err := strconv.Atoi(up[2:])
		if err != nil || n < 2 || n > 24 || n%2 != 0 {
			return Channel{}, fmt.Errorf("CS のチャンネル %q: 2〜24 の偶数（例: CS2）", s)
		}
		return Channel{
			Name:    fmt.Sprintf("CS%d", n),
			Band:    BandCS,
			FreqKHz: csBaseKHz + uint32(n/2-1)*csStepKHz,
			TSID:    csTSIDs[n],
			Slot:    -1,
		}, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 13 || n > 62 {
		return Channel{}, fmt.Errorf("チャンネル %q が読めない（13〜62、BS15_0、CS2 など）", s)
	}
	return Channel{
		Name:    strconv.Itoa(n),
		Band:    BandUHF,
		FreqKHz: uhfBaseKHz + uint32(n-13)*uhfStepKHz,
		Slot:    -1,
	}, nil
}
