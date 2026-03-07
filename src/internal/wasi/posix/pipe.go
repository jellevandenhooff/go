// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package posix

// PipeBuf is a simple in-memory pipe buffer shared between a read end and
// write end. Data written to the write end can be read from the read end.
//
// This is used by the wasip3 exec implementation: StartProcess writes captured
// child output into the pipe, then os/exec goroutines read from it.
// On wasip3, StartProcess blocks asynchronously during child execution, so
// the reader goroutine may run before data is available. PipeBuf.Read blocks
// (via channel) until data is written or the write end is closed.
type PipeBuf struct {
	data   []byte
	pos    int  // read position
	closed bool // write end closed
	// notify is signaled when data is written or write end is closed.
	// Buffered so Write/CloseWrite never block.
	notify chan struct{}
}

func newPipeBuf() *PipeBuf {
	return &PipeBuf{
		notify: make(chan struct{}, 1),
	}
}

// signal sends a non-blocking notification on the channel.
func (p *PipeBuf) signal() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// Write appends data to the pipe buffer.
func (p *PipeBuf) Write(b []byte) (int, error) {
	if p.closed {
		return 0, ErrBADF
	}
	p.data = append(p.data, b...)
	p.signal()
	return len(b), nil
}

// Read reads from the pipe buffer. Blocks if no data is available and the
// write end is still open, so the calling goroutine yields until data arrives.
func (p *PipeBuf) Read(b []byte) (int, error) {
	for p.pos >= len(p.data) && !p.closed {
		// Block until Write or CloseWrite signals us.
		<-p.notify
	}
	if p.pos >= len(p.data) {
		return 0, nil // EOF — write end closed, no more data
	}
	n := copy(b, p.data[p.pos:])
	p.pos += n
	return n, nil
}

// CloseWrite marks the write end as closed, signaling EOF to readers.
func (p *PipeBuf) CloseWrite() {
	p.closed = true
	p.signal()
}

// AllocPipeFDs allocates a read fd and write fd backed by a shared PipeBuf.
// Returns (readFD, writeFD) or (-1, -1) if no fds available.
func AllocPipeFDs() (int, int) {
	rfd := FDAlloc()
	if rfd < 0 {
		return -1, -1
	}
	wfd := FDAlloc()
	if wfd < 0 {
		Free(rfd)
		return -1, -1
	}
	buf := newPipeBuf()
	FDTable[rfd].IsPipe = true
	FDTable[rfd].PipeRead = buf
	FDTable[wfd].IsPipe = true
	FDTable[wfd].PipeWrite = buf
	return rfd, wfd
}
