// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package net

import (
	"os"
	"syscall"
)

func fileListener(f *os.File) (Listener, error) {
	return nil, syscall.ENOTSUP
}

func fileConn(f *os.File) (Conn, error) {
	return nil, syscall.ENOTSUP
}

func filePacketConn(f *os.File) (PacketConn, error) {
	return nil, syscall.ENOTSUP
}
