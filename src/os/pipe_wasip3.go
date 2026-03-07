// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package os

import "internal/wasi/posix"

// Pipe returns a connected pair of Files; reads from r return bytes written to w.
// On wasip3, this uses in-memory pipe buffers.
func Pipe() (r *File, w *File, err error) {
	rfd, wfd := posix.AllocPipeFDs()
	if rfd < 0 {
		return nil, nil, NewSyscallError("pipe", ErrInvalid)
	}
	return NewFile(uintptr(rfd), "|0"), NewFile(uintptr(wfd), "|1"), nil
}
