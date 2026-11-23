// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

package b25

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/waya0125/recfriio/internal/multi2"
)

// 作り物の放送: PAT → PMT（番組 1、PMT は 0x100、CA 記述子で ECM は 0x200）→ 映像 0x111。
// ECM の中身ごとに、作り物のカードが決まった鍵を返す。

const (
	caSystem = 0x0005
	pmtPID   = 0x100
	ecmPID   = 0x200
	videoPID = 0x111
)

var (
	systemKey = bytes.Repeat([]byte{0x11, 0x22, 0x33, 0x44}, 8)
	initCBC   = []byte{1, 2, 3, 4, 5, 6, 7, 8}
)

type fakeCard struct {
	keys  map[string][]byte
	calls int
	fail  bool
	err   error
}

func (c *fakeCard) SystemKey() []byte { return systemKey }
func (c *fakeCard) InitCBC() []byte   { return initCBC }
func (c *fakeCard) CASystemID() int   { return caSystem }
func (c *fakeCard) ProcessECM(ecm []byte) ([]byte, int, error) {
	c.calls++
	if c.err != nil {
		return nil, 0, c.err
	}
	if c.fail {
		return nil, 0, errors.New("カードが答えない")
	}
	k, ok := c.keys[string(ecm)]
	if !ok {
		return nil, 0xa102, nil // 契約していない
	}
	return k, 0x0800, nil
}

func makeSection(table uint8, ext uint16, body []byte) []byte {
	length := 5 + len(body) + 4
	s := []byte{table, 0xb0 | byte(length>>8), byte(length), byte(ext >> 8), byte(ext), 0xc1, 0, 0}
	s = append(s, body...)
	crc := crc32(s)
	return binary.BigEndian.AppendUint32(s, crc)
}

func sectionPacket(pid int, cc int, sec []byte) []byte {
	p := make([]byte, 188)
	p[0], p[1], p[2], p[3] = 0x47, 0x40|byte(pid>>8), byte(pid), 0x10|byte(cc&15)
	p[4] = 0 // pointer_field
	n := copy(p[5:], sec)
	for i := 5 + n; i < 188; i++ {
		p[i] = 0xff
	}
	return p
}

func pat() []byte {
	return makeSection(tablePAT, 1, []byte{0x00, 0x01, 0xe0 | pmtPID>>8, pmtPID & 0xff})
}

func pmt() []byte {
	ca := []byte{0x09, 4, caSystem >> 8, caSystem & 0xff, 0xe0 | ecmPID>>8, ecmPID & 0xff}
	body := []byte{byte(0xe0 | videoPID>>8), byte(videoPID & 0xff), 0xf0, byte(len(ca))}
	body = append(body, ca...)
	body = append(body, 0x02, byte(0xe0|videoPID>>8), byte(videoPID&0xff), 0xf0, 0x00)
	return makeSection(tablePMT, 1, body)
}

func ecm(n byte) []byte {
	return makeSection(tableECMS, 1, bytes.Repeat([]byte{n}, 40))
}

func TestECMErrorKeepsCause(t *testing.T) {
	cause := errors.New("APDUの長さが未対応")
	d := New(&fakeCard{err: cause}, 4, false)
	dec := &decryptor{ecm: newSectionParser()}
	packet := sectionPacket(ecmPID, 0, ecm(1))
	if err := dec.ecm.put(ecmPID, true, packet[4:]); err != nil {
		t.Fatal(err)
	}
	if _, err := d.procECM(dec); !errors.Is(err, cause) || !errors.Is(err, ErrECMProcess) {
		t.Fatalf("ECMエラーの原因を保持していない: %v", err)
	}
}

// videoPacket は scrambling（0: そのまま、2: 偶数、3: 奇数）の映像のパケットと、外したときの答え。
func videoPacket(i int, scrambling int, c *multi2.Cipher) (scrambled, plain []byte) {
	plain = make([]byte, 188)
	plain[0], plain[1], plain[2], plain[3] = 0x47, byte(videoPID>>8), byte(videoPID&0xff), 0x10|byte(i&15)
	for j := 4; j < 188; j++ {
		plain[j] = byte(i*13 + j)
	}
	scrambled = append([]byte{}, plain...)
	if scrambling != 0 {
		c.Encrypt(scrambling, scrambled[4:])
		scrambled[3] |= byte(scrambling) << 6
	}
	return
}

func TestDecodesSyntheticBroadcast(t *testing.T) {
	key1 := []byte("oddkey01evenky01")
	key2 := []byte("oddkey02evenky02")
	card := &fakeCard{keys: map[string][]byte{
		string(ecm(1)[8 : len(ecm(1))-4]): key1,
		string(ecm(2)[8 : len(ecm(2))-4]): key2,
	}}
	c1 := multi2.New(systemKey, initCBC, 4)
	c1.SetScrambleKey(key1)
	c2 := multi2.New(systemKey, initCBC, 4)
	c2.SetScrambleKey(key2)

	var input, want []byte
	add := func(in, out []byte) {
		input = append(input, in...)
		want = append(want, out...)
	}
	same := func(p []byte) { add(p, p) }
	for i := range 400 {
		switch {
		case i%50 == 0:
			same(sectionPacket(0, i/50, pat()))
		case i%50 == 1:
			same(sectionPacket(pmtPID, i/50, pmt()))
		case i%25 == 2:
			n := byte(1)
			if i >= 200 {
				n = 2
			}
			same(sectionPacket(ecmPID, i/25, ecm(n)))
		default:
			c := c1
			if i > 202 {
				c = c2
			}
			s, p := videoPacket(i, 2+i%2, c)
			add(s, p)
		}
	}
	// ビット誤りのパケットはそのまま出る
	tei, _ := videoPacket(999, 3, c2)
	tei[1] |= 0x80
	same(tei)

	d := New(card, 4, false)
	var got []byte
	// arib25 と同じく、最初の Put には 188 の区切りを見分けられるだけの量（9 パケット以上）が要る
	for start := 0; start < len(input); start += 4000 {
		out, err := d.Put(input[start:min(start+4000, len(input))])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, out...)
	}
	rest, err := d.Flush()
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, rest...)
	if len(got) != len(want) {
		t.Fatalf("%d バイト出た（%d のはず）", len(got), len(want))
	}
	for i := 0; i < len(want); i += 188 {
		if !bytes.Equal(got[i:i+188], want[i:i+188]) {
			t.Fatalf("%d 番目のパケットが違う", i/188)
		}
	}
	// 同じ ECM はカードに何度も送らない（変わったときだけ）
	if card.calls != 2 {
		t.Errorf("カードに %d 回送った（2 回のはず）", card.calls)
	}
}

func TestUnpurchasedPassesThrough(t *testing.T) {
	card := &fakeCard{keys: map[string][]byte{}}
	c := multi2.New(systemKey, initCBC, 4)
	c.SetScrambleKey([]byte("0123456789abcdef"))
	var input []byte
	input = append(input, sectionPacket(0, 0, pat())...)
	input = append(input, sectionPacket(pmtPID, 0, pmt())...)
	input = append(input, sectionPacket(ecmPID, 0, ecm(9))...)
	for i := range 20 {
		s, _ := videoPacket(i, 2, c)
		input = append(input, s...)
	}
	d := New(card, 4, false)
	out, err := d.Put(input)
	if err != nil {
		t.Fatal(err)
	}
	rest, _ := d.Flush()
	out = append(out, rest...)
	if !bytes.Equal(out, input) {
		t.Fatal("契約の無い番組は、スクランブルされたまま出るはず")
	}
}

func TestSectionParserSkipsRepeats(t *testing.T) {
	sp := newSectionParser()
	s := pat()
	for range 3 {
		if err := sp.put(0, true, sectionPacket(0, 0, s)[4:]); err != nil {
			t.Fatal(err)
		}
	}
	if sp.count() != 1 {
		t.Fatalf("同じセクションが %d 回出た", sp.count())
	}
	broken := append([]byte{}, s...)
	broken[9] ^= 1 // CRC が合わない
	sp.put(0, true, sectionPacket(0, 0, broken)[4:])
	if sp.count() != 1 {
		t.Fatal("CRC の合わないセクションが出た")
	}
	if got := sp.get(); got == nil || !bytes.Equal(got.raw, s) {
		t.Fatal("中身が違う")
	}
}

func TestCRC32(t *testing.T) {
	// MPEG-2 の CRC32（"123456789" で 0x0376e6e7）
	if got := crc32([]byte("123456789")); got != 0x0376e6e7 {
		t.Fatalf("%#08x", got)
	}
}
