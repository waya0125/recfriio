// SPDX-License-Identifier: MIT
// Copyright (c) 2026 waya0125

package usbfs

import (
	"bytes"
	"testing"
	"unsafe"
)

func TestCollectAndEmitIsoGap(t *testing.T) {
	const (
		isoPackets    = 4
		isoPacketSize = 8
	)
	urbSize := int(unsafe.Sizeof(urb{})) + isoPackets*int(unsafe.Sizeof(isoFrameDesc{}))
	urbSize = (urbSize + 63) &^ 63
	bufSize := isoPackets * isoPacketSize
	mem := make([]byte, urbSize+bufSize)

	u := (*urb)(unsafe.Pointer(&mem[0]))
	u.actualLength = int32(bufSize)

	s := &Stream{
		memory:        mem,
		urbs:          []*urb{u},
		size:          bufSize,
		stride:        urbSize,
		isoPackets:    isoPackets,
		isoPacketSize: isoPacketSize,
		Chunks:        make(chan []byte, 8),
	}

	frames := s.frames(0)
	base := s.offset(0)
	// frame 0: OK ("AAAA")
	frames[0] = isoFrameDesc{length: isoPacketSize, actualLength: 4, status: 0}
	copy(mem[base:], "AAAA")
	// frame 1, 2: 連続エラー（ギャップ通知は 1 回にまとめる）
	frames[1] = isoFrameDesc{length: isoPacketSize, actualLength: 0, status: 1}
	frames[2] = isoFrameDesc{length: isoPacketSize, actualLength: 0, status: 1}
	// frame 3: OK ("BBBB")
	frames[3] = isoFrameDesc{length: isoPacketSize, actualLength: 4, status: 0}
	copy(mem[base+3*isoPacketSize:], "BBBB")

	s.collectAndEmit(0)
	close(s.Chunks)

	var got [][]byte
	for c := range s.Chunks {
		got = append(got, c)
	}
	if s.FrameErrors != 2 {
		t.Fatalf("FrameErrors=%d, want 2", s.FrameErrors)
	}
	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3 (before, nil gap, after)", len(got))
	}
	if !bytes.Equal(got[0], []byte("AAAA")) || got[1] != nil || !bytes.Equal(got[2], []byte("BBBB")) {
		t.Fatalf("got=%q, want [AAAA, nil, BBBB]", got)
	}
}
