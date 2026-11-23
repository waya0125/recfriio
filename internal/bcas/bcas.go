// このファイルの命令と答えの読み方は、arib25（ARIB STD-B25 仕様確認テストプログラム、茂木 和洋 作）の
// b_cas_card.c を Go に移したもの。arib25 の利用条件（LICENSES/arib25.txt）に同意して作った二次的著作物。
// 移植: waya0125（2026）。移植で足した部分は MIT。

// Package bcas は B-CAS カードに命令を送る（初期化と ECM）。カードとのやり取りの口は Transport。
package bcas

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Transport はカードとのやり取りの口（内蔵スロットの asie5606.Reader、外付けの PC/SC）。
type Transport interface {
	// Reset はカードを初めからやり直す（電源を入れ直して ATR を受ける）。
	Reset() error
	// Transmit は APDU を送り、答え（SW1 SW2 を含む）を受け取る。
	Transmit(apdu []byte) ([]byte, error)
}

// ErrAPDULength は通信方式の上限を超え、リセットしても送れないAPDUを表す。
var ErrAPDULength = errors.New("APDUの長さが未対応")

var (
	cmdInitialSetting = []byte{0x90, 0x30, 0x00, 0x00, 0x00}
	ecmHeader         = []byte{0x90, 0x34, 0x00, 0x00}
)

// ecmRetries は ECM の処理に失敗したときに、カードをつなぎ直して試す回数（arib25 と同じ）。
const ecmRetries = 10

// Card は B-CAS カード。
type Card struct {
	t          Transport
	ecmRetries int
	systemKey  []byte
	initCBC    []byte
	caSystemID int
	cardStatus int
	cardID     uint64
	ok         bool
}

// Open はカードをつなぎ、初期設定の条件を読む。
func Open(t Transport) (*Card, error) {
	return OpenWithECMRetries(t, ecmRetries)
}

// OpenWithECMRetries は、ECM の失敗時にカードを再接続して試す回数を指定して開く。
func OpenWithECMRetries(t Transport, retries int) (*Card, error) {
	c := &Card{t: t, ecmRetries: retries}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Card) connect() error {
	c.ok = false
	if err := c.t.Reset(); err != nil {
		return fmt.Errorf("B-CAS カードをリセットできない: %w", err)
	}
	r, err := c.t.Transmit(cmdInitialSetting)
	if err != nil {
		return fmt.Errorf("B-CAS カードの初期設定を読めない: %w", err)
	}
	if len(r) < 57 {
		return fmt.Errorf("B-CAS カードの初期設定の答えが短い（%d バイト）", len(r))
	}
	if code := binary.BigEndian.Uint16(r[4:]); code != 0x2100 {
		return fmt.Errorf("B-CAS カードの初期設定の答えの番号が %#04x", code)
	}
	c.systemKey = append([]byte{}, r[16:48]...)
	c.initCBC = append([]byte{}, r[48:56]...)
	c.cardID = uint64(r[8])<<40 | uint64(r[9])<<32 | uint64(binary.BigEndian.Uint32(r[10:]))
	c.cardStatus = int(binary.BigEndian.Uint16(r[2:]))
	c.caSystemID = int(binary.BigEndian.Uint16(r[6:]))
	c.ok = true
	return nil
}

// SystemKey はシステム鍵（32 バイト）。
func (c *Card) SystemKey() []byte { return c.systemKey }

// InitCBC は CBC の初期値（8 バイト）。
func (c *Card) InitCBC() []byte { return c.initCBC }

// CASystemID は CA システムの番号。
func (c *Card) CASystemID() int { return c.caSystemID }

// ProcessECM は ECM をカードに渡してスクランブル鍵を受け取る。
// 失敗したら、カードをつなぎ直して指定された回数まで試す（既定は 10 回）。
func (c *Card) ProcessECM(ecm []byte) ([]byte, int, error) {
	if len(ecm) < 1 || len(ecm) > 0xff {
		return nil, 0, errors.New("ECM の長さがおかしい")
	}
	command := append(append(append([]byte{}, ecmHeader...), byte(len(ecm))), ecm...)
	command = append(command, 0)
	r, err := c.t.Transmit(command)
	for retry := 0; (err != nil || len(r) < 25) && retry < c.ecmRetries; retry++ {
		if errors.Is(err, ErrAPDULength) {
			return nil, 0, err
		}
		if cerr := c.connect(); cerr != nil {
			err = cerr
			continue
		}
		r, err = c.t.Transmit(command)
	}
	if err != nil {
		return nil, 0, err
	}
	if len(r) < 25 {
		return nil, 0, fmt.Errorf("ECM の答えが短い（%d バイト）", len(r))
	}
	return append([]byte{}, r[6:22]...), int(binary.BigEndian.Uint16(r[4:])), nil
}
