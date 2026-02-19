// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package unix

import (
	"internal/wasi/posix"
	"syscall"
)

func Fcntl(sysfd int, cmd int, arg int) (int, error) {
	if cmd == syscall.F_GETFL {
		flags, err := posix.FDStatGetFlags(sysfd)
		return int(flags), err
	}
	return 0, syscall.ENOSYS
}
