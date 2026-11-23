// このファイルは arib25（ARIB STD-B25 仕様確認テストプログラム、茂木 和洋 作）の arib_std_b25.c を
// Go に移したもの。arib25 の利用条件（LICENSES/arib25.txt）に同意して作った二次的著作物。
// 移植: waya0125（2026）。移植で足した部分は MIT。
//
// arib25 と同じ出力になるよう、PAT・PMT・ECM を探す順や、復号器の割り当て方はそのまま移した。
// 違うところ:
//   - EMM の処理（--EMM）は持たない（CAT から EMM の PID を覚えるところまでは同じ）。
//   - 途中で失敗したとき（ECM の処理など）や PAT が変わったとき、arib25 はそのパケットを次の put で
//     出力に入れ直してしまう（同じパケットが 2 回出る）。ここでは入れ直さない。
//   - ECM の処理に失敗して鍵が消えたあと、arib25 はスクランブルされたパケットで止まり続けるが、
//     ここではそのまま（スクランブルされたまま）出して、次の ECM を待つ。

// Package b25 は ARIB STD-B25 のスクランブル（MULTI2）を B-CAS カードで外す。
package b25

import (
	"errors"
	"fmt"

	"github.com/waya0125/recfriio/internal/multi2"
)

// Card は B-CAS カード（bcas.Card）。
type Card interface {
	// SystemKey・InitCBC・CASystemID は、カードを初期化したときの値。
	SystemKey() []byte
	InitCBC() []byte
	CASystemID() int
	// ProcessECM は ECM（セクションの見出しのあとから CRC の前まで）を渡し、スクランブル鍵（16 バイト）と
	// カードの返答の番号を受け取る。
	ProcessECM(ecm []byte) (key []byte, returnCode int, err error)
}

// 失敗（arib25 の負のエラー）
var (
	ErrNonTS        = errors.New("TS ではない入力")
	ErrNoPAT        = errors.New("先頭の 16MB に PAT が無い")
	ErrNoPMT        = errors.New("先頭の 32MB に PMT が無い")
	ErrNoECM        = errors.New("先頭の 32MB に ECM が無い")
	ErrECMProcess   = errors.New("B-CAS カードで ECM を処理できない")
	ErrSectionParse = errors.New("セクションを読めない")
)

const (
	tableECMS = 0x82
	tablePAT  = 0x00
	tablePMT  = 0x02
	tableCAT  = 0x01
)

// PID の種類
const (
	pidUnknown = 0x0000
	pidPAT     = 0x0100
	pidPMT     = 0x0200
	pidECM     = 0x0500
	pidEMM     = 0x0600
	pidCAT     = 0x0800
	pidOther   = 0xff00
)

type pidMap struct {
	ref       uint32 // arib25 と同じく符号なし（0 から引くと大きな数になり、消されない）
	typ       int
	decryptor *decryptor
	program   *program
}

type stream struct{ pid, typ int }

type program struct {
	number  int
	pmtPID  int
	pmt     *sectionParser
	phase   int
	streams []stream
	old     []stream
}

type decryptor struct {
	ref         int
	phase       int
	locked      int
	ecmPID      int
	ecm         *sectionParser
	m2          *multi2.Cipher
	unpurchased int
	lastError   int
}

// Decoder は arib25 の ARIB_STD_B25 にあたる。
type Decoder struct {
	round int
	strip bool
	card  Card

	unitSize   int
	sbuf       []byte
	sbufOffset int
	dbuf       []byte

	pat      *sectionParser
	cat      *sectionParser
	programs []*program
	decrypt  []*decryptor
	pids     [0x2000]pidMap
}

// New は card で鍵を得る Decoder を作る。round は MULTI2 の段数（普通は 4）、
// strip ならヌルパケットを捨てる。
func New(card Card, round int, strip bool) *Decoder {
	return &Decoder{round: round, strip: strip, card: card}
}

// Put は TS を渡し、出せるようになった分を返す。
func (d *Decoder) Put(data []byte) ([]byte, error) {
	d.sbuf = append(d.sbuf, data...)
	if d.unitSize < 188 {
		if err := d.selectUnitSize(); err != nil {
			return nil, err
		}
		if d.unitSize < 188 {
			return nil, nil
		}
	}
	if len(d.programs) < 1 {
		if err := d.findPAT(); err != nil {
			return nil, err
		}
		if len(d.programs) < 1 {
			if d.sbufOffset < 16*1024*1024 {
				return nil, nil
			}
			return nil, ErrNoPAT
		}
		d.sbufOffset = 0
	}
	if !d.pmtComplete() {
		if err := d.findPMT(); err != nil {
			return nil, err
		}
		if !d.pmtComplete() {
			if d.sbufOffset < 32*1024*1024 {
				return nil, nil
			}
			return nil, ErrNoPMT
		}
		d.sbufOffset = 0
	}
	if !d.ecmComplete() {
		if err := d.findECM(); err != nil {
			return nil, err
		}
		if !d.ecmComplete() {
			if d.sbufOffset < 32*1024*1024 {
				return nil, nil
			}
			return nil, ErrNoECM
		}
		d.sbufOffset = 0
	}
	err := d.process(false)
	return d.take(), err
}

// Flush は残りを全部処理して返す（入力の終わりで呼ぶ）。
func (d *Decoder) Flush() ([]byte, error) {
	if d.unitSize < 188 {
		if err := d.selectUnitSize(); err != nil {
			return nil, err
		}
	}
	if err := d.process(false); err != nil {
		return d.take(), err
	}
	err := d.process(true)
	return d.take(), err
}

func (d *Decoder) take() []byte {
	out := d.dbuf
	d.dbuf = nil
	return out
}

func (d *Decoder) selectUnitSize() error {
	var count [320 - 188]int
	buf := d.sbuf
	for i := 0; i+188 < len(buf); i++ {
		if buf[i] != 0x47 {
			continue
		}
		m := min(320, len(buf)-i)
		for j := 188; j < m; j++ {
			if buf[i+j] == 0x47 {
				count[j-188]++
			}
		}
	}
	m, n := 0, 0
	for i := 188; i < 320; i++ {
		if m < count[i-188] {
			m = count[i-188]
			n = i
		}
	}
	if m < 8 || m*n+3*n < len(buf) {
		return ErrNonTS
	}
	d.unitSize = n
	return nil
}

type header struct {
	tei, unitStart       bool
	pid                  int
	scrambling, adaption int
}

func readHeader(p []byte) header {
	return header{
		tei:        p[1]&0x80 != 0,
		unitStart:  p[1]&0x40 != 0,
		pid:        int(p[1]&0x1f)<<8 | int(p[2]),
		scrambling: int(p[3]>>6) & 3,
		adaption:   int(p[3]>>4) & 3,
	}
}

// payload はパケットのペイロードの始まりの位置と長さ（負なら壊れている）。
func payload(p []byte, h header) (start, size int) {
	start = 4
	if h.adaption&2 != 0 {
		start += int(p[4]) + 1
	}
	return start, 188 - start
}

// resync は unit ごとに 0x47 が 8 回続くところを探す。見つからなければ -1。
func resync(buf []byte, from, unit int) int {
	last := len(buf) - unit*8
	for i := from; i <= last; i++ {
		if buf[i] != 0x47 {
			continue
		}
		ok := true
		for k := 1; k < 8; k++ {
			if buf[i+unit*k] != 0x47 {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// resyncForce は残りが少ないとき（Flush）の resync。
func resyncForce(buf []byte, from, unit int) int {
	for i := from; i <= len(buf)-188; i++ {
		if buf[i] != 0x47 {
			continue
		}
		n := (len(buf) - i) / unit
		if n == 0 {
			return i
		}
		k := 1
		for ; k < n; k++ {
			if buf[i+unit*k] != 0x47 {
				break
			}
		}
		if k == n {
			return i
		}
	}
	return -1
}

// scan は PAT・PMT・ECM を探す 3 つの段の共通の回り方。visit が true を返したら止める。
func (d *Decoder) scan(visit func(p []byte, h header) (stop bool, err error)) error {
	unit := d.unitSize
	curr := d.sbufOffset
	var err error
	for curr+unit < len(d.sbuf) {
		if d.sbuf[curr] != 0x47 || d.sbuf[curr+unit] != 0x47 {
			p := resync(d.sbuf, curr, unit)
			if p < 0 {
				break
			}
			curr = p
		}
		var stop bool
		stop, err = visit(d.sbuf[curr:curr+188], readHeader(d.sbuf[curr:]))
		if stop {
			curr += unit
			break
		}
		curr += unit
	}
	d.sbufOffset = curr
	return err
}

func (d *Decoder) findPAT() error {
	err := d.scan(func(p []byte, h header) (bool, error) {
		if h.pid != 0 {
			return false, nil
		}
		start, size := payload(p, h)
		if size < 1 {
			return false, nil
		}
		if d.pat == nil {
			d.pat = newSectionParser()
		}
		if err := d.pat.put(h.pid, h.unitStart, p[start:]); err != nil {
			return true, ErrSectionParse
		}
		return d.pat.count() > 0, nil
	})
	if err != nil {
		return err
	}
	if d.pat != nil && d.pat.count() > 0 {
		d.procPAT()
	}
	return nil
}

func (d *Decoder) procPAT() {
	s := d.pat.get()
	if s == nil || s.tableID != tablePAT {
		return
	}
	body := s.data()
	body = body[:max(0, len(body)-4)]
	for _, p := range d.programs {
		d.releaseProgram(p)
	}
	d.programs = nil
	d.pids = [0x2000]pidMap{}
	for i := 0; i+4 <= len(body); i += 4 {
		number := int(body[i])<<8 | int(body[i+1])
		pid := (int(body[i+2])<<8 | int(body[i+3])) & 0x1fff
		if number == 0 {
			continue
		}
		p := &program{number: number, pmtPID: pid, pmt: newSectionParser()}
		d.programs = append(d.programs, p)
		d.pids[pid].typ = pidPMT
		d.pids[pid].program = p
	}
	d.pids[0] = pidMap{ref: 1, typ: pidPAT}
}

func (d *Decoder) pmtComplete() bool {
	var num [3]int
	for _, p := range d.programs {
		num[min(max(p.phase, 0), 2)]++
	}
	return num[2] > 0 || num[0] == 0
}

func (d *Decoder) findPMT() error {
	return d.scan(func(p []byte, h header) (bool, error) {
		m := &d.pids[h.pid]
		if m.typ != pidPMT || m.program == nil {
			return false, nil
		}
		pg := m.program
		if pg.phase != 0 {
			pg.phase = 2
			return true, nil
		}
		start, size := payload(p, h)
		if size < 1 {
			return false, nil
		}
		if err := pg.pmt.put(h.pid, h.unitStart, p[start:]); err != nil {
			return true, ErrSectionParse
		}
		if pg.pmt.count() == 0 {
			return false, nil
		}
		if !d.procPMT(pg) {
			return false, nil // 壊れたセクション
		}
		pg.phase = 1
		return d.pmtComplete(), nil
	})
}

// procPMT は PMT を読んで復号器を割り当てる。セクションが壊れていれば false。
func (d *Decoder) procPMT(pg *program) bool {
	s := pg.pmt.get()
	if s == nil || s.tableID != tablePMT {
		return false
	}
	body := s.data()
	if len(body) < 4 {
		return false
	}
	tail := len(body) - 4
	length := (int(body[2])<<8 | int(body[3])) & 0x0fff
	head := 4
	if head+length > tail {
		return false
	}
	var major *decryptor
	ecmPID := findCADescriptorPID(body[head:head+length], d.card.CASystemID())
	if ecmPID != 0 && ecmPID != 0x1fff {
		major = d.setDecryptor(ecmPID)
		major.ref++
	} else if len(d.decrypt) == 1 {
		major = d.decrypt[0]
		major.ref++
	}
	head += length

	for _, st := range pg.old {
		d.unrefStream(st.pid)
	}
	pg.old = pg.streams
	pg.streams = nil
	if ecmPID != 0 && ecmPID != 0x1fff {
		d.addECMStream(pg, ecmPID)
	}
	for head+4 < tail {
		typ := int(body[head])
		pid := (int(body[head+1])<<8 | int(body[head+2])) & 0x1fff
		length := (int(body[head+3])<<8 | int(body[head+4])) & 0x0fff
		head += 5
		end := min(head+length, len(body))
		esECM := findCADescriptorPID(body[head:end], d.card.CASystemID())
		head += length
		var own *decryptor
		if esECM != 0 && esECM != 0x1fff {
			own = d.setDecryptor(esECM)
			d.addECMStream(pg, esECM)
		}
		d.pids[pid].typ = pidOther
		d.pids[pid].ref++
		d.bindStream(pid, selectActive(major, own, esECM))
		pg.streams = append(pg.streams, stream{pid: pid, typ: typ})
	}
	if major != nil {
		major.ref--
		if major.ref < 1 {
			d.removeDecryptor(major)
		}
	}
	return true
}

func findCADescriptorPID(p []byte, caSystemID int) int {
	for i := 0; i+1 < len(p); {
		tag, length := p[i], int(p[i+1])
		i += 2
		if tag == 0x09 && length >= 4 && i+length <= len(p) {
			system := int(p[i])<<8 | int(p[i+1])
			pid := (int(p[i+2])<<8 | int(p[i+3])) & 0x1fff
			if system == caSystemID {
				return pid
			}
		}
		i += length
	}
	return 0
}

func (d *Decoder) addECMStream(pg *program, ecmPID int) {
	for _, st := range pg.streams {
		if st.pid == ecmPID {
			return
		}
	}
	pg.streams = append(pg.streams, stream{pid: ecmPID, typ: pidECM})
	d.pids[ecmPID].ref++
}

func (d *Decoder) ecmComplete() bool {
	var num [3]int
	for _, e := range d.decrypt {
		num[min(max(e.phase, 0), 2)]++
	}
	return num[2] > 0 || num[0] == 0
}

func (d *Decoder) findECM() error {
	return d.scan(func(p []byte, h header) (bool, error) {
		m := &d.pids[h.pid]
		if m.typ != pidECM || m.decryptor == nil {
			return false, nil
		}
		dec := m.decryptor
		if dec.phase != 0 {
			dec.phase = 2
			return true, nil
		}
		start, size := payload(p, h)
		if size < 1 {
			return false, nil
		}
		if err := dec.ecm.put(h.pid, h.unitStart, p[start:]); err != nil {
			return true, ErrSectionParse
		}
		if dec.ecm.count() == 0 {
			return false, nil
		}
		warn, err := d.procECM(dec)
		if err != nil {
			return true, err
		}
		if warn != 0 && warn != warnUnpurchased {
			return false, nil
		}
		dec.phase = 1
		return d.ecmComplete(), nil
	})
}

const (
	warnUnpurchased = 1
	warnTableID     = 2
)

func (d *Decoder) procECM(dec *decryptor) (warn int, err error) {
	s := dec.ecm.get()
	if s == nil {
		return 0, ErrSectionParse
	}
	if s.tableID != tableECMS {
		return warnTableID, nil
	}
	if dec.locked != 0 {
		dec.unpurchased++
		return warnUnpurchased, nil
	}
	body := s.data()
	ecm := body[:max(0, len(body)-4)]
	key, code, err := d.card.ProcessECM(ecm)
	if err != nil {
		if dec.m2 != nil {
			dec.m2.ClearScrambleKey()
		}
		return 0, fmt.Errorf("%w: %w", ErrECMProcess, err)
	}
	if code != 0x0800 && code != 0x0400 && code != 0x0200 {
		// 契約していない
		dec.m2 = nil
		dec.unpurchased++
		dec.lastError = code
		dec.locked++
		return warnUnpurchased, nil
	}
	if dec.m2 == nil {
		dec.m2 = multi2.New(d.card.SystemKey(), d.card.InitCBC(), d.round)
	}
	dec.m2.SetScrambleKey(key)
	return 0, nil
}

// process は arib25 の proc_arib_std_b25（flush なら flush_arib_std_b25）。
func (d *Decoder) process(flush bool) error {
	unit := d.unitSize
	curr := 0
	buf := d.sbuf
	var err error
	for (!flush && curr+unit < len(buf)) || (flush && curr+188 <= len(buf)) {
		if flush {
			if buf[curr] != 0x47 {
				p := resyncForce(buf, curr, unit)
				if p < 0 {
					break
				}
				curr = p
			}
		} else if buf[curr] != 0x47 || buf[curr+unit] != 0x47 {
			p := resync(buf, curr, unit)
			if p < 0 {
				break
			}
			curr = p
		}
		packet := buf[curr : curr+188]
		h := readHeader(packet)
		if h.tei {
			// ビット誤り。そのまま出す
			d.dbuf = append(d.dbuf, packet...)
			curr += unit
			continue
		}
		if h.pid == 0x1fff && d.strip {
			curr += unit
			continue
		}
		start, size := payload(packet, h)
		if size < 1 && (size < 0 || h.adaption&1 != 0) {
			// 壊れたパケット
			curr++
			continue
		}
		m := &d.pids[h.pid]
		if h.scrambling != 0 && h.adaption&1 != 0 {
			var dec *decryptor
			if m.typ == pidOther {
				dec = m.decryptor
			} else if m.typ == pidUnknown && len(d.decrypt) == 1 {
				dec = d.decrypt[0]
			}
			if dec != nil && dec.m2 != nil && dec.m2.Keyed() {
				dec.m2.Decrypt(h.scrambling, packet[start:])
				packet[3] &= 0x3f
			}
		}
		d.dbuf = append(d.dbuf, packet...)
		data := packet[start:]

		var stop bool
		switch {
		case m.typ == pidECM:
			dec := m.decryptor
			if dec == nil {
				err, stop = ErrSectionParse, true
				break
			}
			if e := dec.ecm.put(h.pid, h.unitStart, data); e != nil {
				err, stop = ErrSectionParse, true
				break
			}
			if dec.ecm.count() > 0 {
				if _, e := d.procECM(dec); e != nil {
					err, stop = e, true
				}
			}
		case m.typ == pidPMT:
			pg := m.program
			if pg == nil {
				err, stop = ErrSectionParse, true
				break
			}
			if e := pg.pmt.put(h.pid, h.unitStart, data); e != nil {
				err, stop = ErrSectionParse, true
				break
			}
			if pg.pmt.count() > 0 {
				d.procPMT(pg)
			}
		case m.typ == pidEMM:
			// EMM は処理しない
		case h.pid == 0x0001:
			if d.cat == nil {
				d.cat = newSectionParser()
			}
			if e := d.cat.put(h.pid, h.unitStart, data); e != nil {
				err, stop = ErrSectionParse, true
				break
			}
			if d.cat.count() > 0 {
				d.procCAT()
			}
		case h.pid == 0x0000:
			if d.pat == nil {
				d.pat = newSectionParser()
			}
			if e := d.pat.put(h.pid, h.unitStart, data); e != nil {
				err, stop = ErrSectionParse, true
				break
			}
			if d.pat.count() > 0 {
				d.procPAT()
				if !flush {
					// arib25 は PAT が変わったら、PMT・ECM を探し直すためにここで止める
					curr += unit
					stop = true
				}
			}
		}
		if stop {
			if err != nil {
				// ここで止めても、このパケットはもう出力に入れた（arib25 はここで進まずに入れ直す）
				curr += unit
			}
			break
		}
		curr += unit
	}
	d.sbuf = append(d.sbuf[:0:0], buf[min(curr, len(buf)):]...)
	return err
}

func (d *Decoder) procCAT() {
	s := d.cat.get()
	if s == nil || s.tableID != tableCAT {
		return
	}
	body := s.data()
	emmPID := findCADescriptorPID(body[:max(0, len(body)-4)], d.card.CASystemID())
	if emmPID != 0 && emmPID != 0x1fff {
		m := &d.pids[emmPID]
		if m.decryptor != nil && m.typ == pidOther {
			m.decryptor.ref--
			if m.decryptor.ref < 1 {
				d.removeDecryptor(m.decryptor)
			}
		}
		*m = pidMap{ref: 1, typ: pidEMM}
	}
	d.pids[1] = pidMap{ref: 1, typ: pidCAT}
}

func (d *Decoder) releaseProgram(pg *program) {
	for _, st := range pg.old {
		d.unrefStream(st.pid)
	}
	for _, st := range pg.streams {
		d.unrefStream(st.pid)
	}
	pg.old, pg.streams = nil, nil
	d.pids[pg.pmtPID] = pidMap{}
}

func (d *Decoder) unrefStream(pid int) {
	m := &d.pids[pid]
	m.ref--
	if m.ref < 1 {
		if m.decryptor != nil && m.typ == pidOther {
			m.decryptor.ref--
			if m.decryptor.ref < 1 {
				d.removeDecryptor(m.decryptor)
			}
		}
		*m = pidMap{}
	}
}

func (d *Decoder) setDecryptor(pid int) *decryptor {
	m := &d.pids[pid]
	if m.typ == pidECM && m.decryptor != nil {
		return m.decryptor
	}
	r := &decryptor{ecmPID: pid, ecm: newSectionParser()}
	d.decrypt = append(d.decrypt, r)
	if m.typ == pidOther && m.decryptor != nil {
		m.decryptor.ref--
		if m.decryptor.ref < 1 {
			d.removeDecryptor(m.decryptor)
		}
	}
	m.typ = pidECM
	m.decryptor = r
	return r
}

func (d *Decoder) removeDecryptor(dec *decryptor) {
	m := &d.pids[dec.ecmPID]
	if m.typ == pidECM && m.decryptor == dec {
		m.typ = pidUnknown
		m.decryptor = nil
	}
	for i, e := range d.decrypt {
		if e == dec {
			d.decrypt = append(d.decrypt[:i:i], d.decrypt[i+1:]...)
			break
		}
	}
	dec.ecm = nil
	dec.m2 = nil
}

func selectActive(major, own *decryptor, pid int) *decryptor {
	if own != nil {
		return own
	}
	if pid == 0x1fff {
		return nil
	}
	return major
}

func (d *Decoder) bindStream(pid int, dec *decryptor) {
	m := &d.pids[pid]
	old := m.decryptor
	if old == dec {
		return
	}
	if old != nil {
		old.ref--
		if old.ref == 0 {
			d.removeDecryptor(old)
		}
		m.decryptor = nil
	}
	if dec != nil {
		m.decryptor = dec
		dec.ref++
	}
}
