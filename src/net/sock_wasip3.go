// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package net

import "os"

// sysSocket wraps socketFunc without CLOEXEC/NONBLOCK setup,
// which are not applicable to WASI file descriptors.
func sysSocket(family, sotype, proto int) (int, error) {
	s, err := socketFunc(family, sotype, proto)
	if err != nil {
		return -1, os.NewSyscallError("socket", err)
	}
	return s, nil
}
