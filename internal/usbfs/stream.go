// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125
// Copyright (c) 2026 waya0125

package usbfs

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// Stream はバルクか isochronous の IN エンドポイントから受け続ける。
// URB を何個も投げておき、届いたものから順に Chunks に流して、すぐ投げ直す。
// 受け取りが遅れても USB 側の受信が止まらないよう、Chunks には余裕を持たせる。
//
// URB と受け取りの入れ物は mmap で取った Go の外のメモリに置く
// （カーネルが後から書き込むので、Go のヒープには置かない）。
type Stream struct {
	device *Device
	memory []byte
	urbs   []*urb
	byAddr map[uintptr]int
	size   int
	// stride は URB 1 個ぶんの置き場の大きさ（isochronous ならフレームの記述も含む）
	stride int
	// isochronous のとき: 1 URB のフレーム数と、1 フレームの大きさ。バルクなら 0
	isoPackets    int
	isoPacketSize int

	// Chunks に、届いた順にデータが流れる。空のスライス（nil）は転送の欠落（ギャップ）の通知。Stop のあとで閉じられる。
	Chunks chan []byte

	mu          sync.Mutex
	stopping    bool
	outstanding int
	done        chan struct{}

	// 数え（Stop のあとで読む）
	Dropped int // Chunks がいっぱいで捨てた回数
	Errors  int // 失敗で終わった URB の数
	// FrameErrors は isochronous で失敗したフレームの数（そのフレームは捨てる）
	FrameErrors int
	// LastError は最後に失敗した URB の状態
	LastError error
}

// StartStream は endpoint に count 個のバルクの URB（1 個 size バイト）を投げて受け始める。
// queue は Chunks に溜められる数。
func (d *Device) StartStream(endpoint uint8, count, size, queue int) (*Stream, error) {
	return d.startStream(endpoint, count, size, queue, 0, 0)
}

// StartIsoStream は endpoint に count 個の isochronous の URB を投げて受け始める。
// 1 個の URB は packets フレーム、1 フレームは packetSize バイト。
// Chunks には、うまく届いたフレームの中身だけを順につないで流す。
func (d *Device) StartIsoStream(endpoint uint8, count, packetSize, packets, queue int) (*Stream, error) {
	if packetSize <= 0 || packets <= 0 {
		return nil, errors.New("isochronous のフレームの数と大きさは正にする")
	}
	return d.startStream(endpoint, count, packetSize*packets, queue, packetSize, packets)
}

func (d *Device) startStream(endpoint uint8, count, size, queue, isoPacketSize, isoPackets int) (*Stream, error) {
	if count <= 0 || size <= 0 {
		return nil, errors.New("URB の数と大きさは正にする")
	}
	urbSize := int(unsafe.Sizeof(urb{})) + isoPackets*int(unsafe.Sizeof(isoFrameDesc{}))
	urbSize = (urbSize + 63) &^ 63
	total := count * (urbSize + size)
	memory, err := syscall.Mmap(-1, 0, total, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		return nil, fmt.Errorf("受信の入れ物を取れない: %w", err)
	}
	s := &Stream{
		device: d, memory: memory, size: size, stride: urbSize,
		isoPackets: isoPackets, isoPacketSize: isoPacketSize,
		urbs:   make([]*urb, count),
		byAddr: make(map[uintptr]int, count),
		Chunks: make(chan []byte, queue),
		done:   make(chan struct{}),
	}
	buffers := count * urbSize
	for i := range count {
		u := (*urb)(unsafe.Pointer(&memory[i*urbSize]))
		*u = urb{
			typ:          urbTypeBulk,
			endpoint:     endpoint,
			buffer:       uintptr(unsafe.Pointer(&memory[buffers+i*size])),
			bufferLength: int32(size),
		}
		if isoPackets > 0 {
			u.typ = urbTypeIso
			u.flags = urbFlagIsoASAP
			u.numberOfPackets = int32(isoPackets)
		}
		s.urbs[i] = u
		s.byAddr[uintptr(unsafe.Pointer(u))] = i
	}
	for i := range count {
		if err := s.submit(i); err != nil {
			s.mu.Lock()
			s.stopping = true
			s.mu.Unlock()
			s.discardAll()
			for s.outstanding > 0 {
				if _, ok := s.reap(); !ok {
					break
				}
				s.outstanding--
			}
			_ = syscall.Munmap(memory)
			return nil, fmt.Errorf("URB を投げられない: %w", err)
		}
		s.outstanding++
	}
	go s.run()
	return s, nil
}

// frames は URB i のフレームの記述（isochronous のときだけ）。
func (s *Stream) frames(i int) []isoFrameDesc {
	if s.isoPackets == 0 {
		return nil
	}
	base := unsafe.Pointer(uintptr(unsafe.Pointer(s.urbs[i])) + unsafe.Sizeof(urb{}))
	return unsafe.Slice((*isoFrameDesc)(base), s.isoPackets)
}

func (s *Stream) submit(i int) error {
	u := s.urbs[i]
	u.status = 0
	u.actualLength = 0
	u.startFrame = 0
	u.errorCount = 0
	frames := s.frames(i)
	for f := range frames {
		frame := &frames[f]
		frame.length = uint32(s.isoPacketSize)
		frame.actualLength = 0
		frame.status = 0
	}
	_, err := ioctl(s.device.fd, ioctlSubmitURB, uintptr(unsafe.Pointer(u)))
	return err
}

// reap は終わった URB を 1 つ待って、その番号を返す。
func (s *Stream) reap() (int, bool) {
	var addr uintptr
	if _, err := ioctl(s.device.fd, ioctlReapURB, uintptr(unsafe.Pointer(&addr))); err != nil {
		return 0, false
	}
	i, ok := s.byAddr[addr]
	return i, ok
}

func (s *Stream) run() {
	defer close(s.done)
	defer close(s.Chunks)
	for {
		s.mu.Lock()
		left := s.outstanding
		s.mu.Unlock()
		if left == 0 {
			return
		}
		i, ok := s.reap()
		if !ok {
			// 機器が抜かれたなど。投げたものはもう戻らない
			s.mu.Lock()
			s.outstanding = 0
			s.mu.Unlock()
			return
		}
		u := s.urbs[i]
		s.mu.Lock()
		stopping := s.stopping
		s.mu.Unlock()
		if !stopping {
			if u.status != 0 {
				s.Errors++
				s.LastError = syscall.Errno(-u.status)
				s.sendChunk(nil)
			}
			s.collectAndEmit(i)
		}
		s.mu.Lock()
		if s.stopping {
			s.outstanding--
		} else if err := s.submit(i); err != nil {
			s.Errors++
			s.LastError = err
			s.outstanding--
		}
		s.mu.Unlock()
	}
}

func (s *Stream) sendChunk(chunk []byte) {
	select {
	case s.Chunks <- chunk:
	default:
		s.Dropped++
	}
}

// collectAndEmit は URB i に届いたデータを Chunks へ流す。
// isochronous なら、うまく届いたフレームを順につなぎ、失敗したフレームの境目で nil（ギャップ通知）を挟む。
func (s *Stream) collectAndEmit(i int) {
	u := s.urbs[i]
	base := s.offset(i)
	if s.isoPackets == 0 {
		n := int(u.actualLength)
		if n <= 0 {
			return
		}
		chunk := make([]byte, n)
		copy(chunk, s.memory[base:base+n])
		s.sendChunk(chunk)
		return
	}
	var chunk []byte
	inGap := false
	for f, frame := range s.frames(i) {
		if frame.status != 0 {
			s.FrameErrors++
			if len(chunk) > 0 {
				s.sendChunk(chunk)
				chunk = nil
			}
			if !inGap {
				s.sendChunk(nil)
				inGap = true
			}
			continue
		}
		inGap = false
		n := min(int(frame.actualLength), s.isoPacketSize)
		if n > 0 {
			if chunk == nil {
				chunk = make([]byte, 0, int(u.actualLength))
			}
			start := base + f*s.isoPacketSize
			chunk = append(chunk, s.memory[start:start+n]...)
		}
	}
	if len(chunk) > 0 {
		s.sendChunk(chunk)
	}
}

func (s *Stream) offset(i int) int {
	return len(s.urbs)*s.stride + i*s.size
}

func (s *Stream) discardAll() {
	for _, u := range s.urbs {
		// 終わっていて投げ直していないものは EINVAL になるが、それでよい
		_, _ = ioctl(s.device.fd, ioctlDiscardURB, uintptr(unsafe.Pointer(u)))
	}
}

// Stop は受信を止め、投げた URB が全部戻るのを待ってから入れ物を返す。
func (s *Stream) Stop() {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		<-s.done
		return
	}
	s.stopping = true
	s.discardAll()
	s.mu.Unlock()
	<-s.done
	runtime.KeepAlive(s.urbs)
	_ = syscall.Munmap(s.memory)
	s.memory = nil
}
