// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package unix

import (
	"internal/wasi/posix"
	"syscall"
)

func IsNonblock(sysfd int) (nonblocking bool, err error) {
	flags, e1 := posix.FDStatGetFlags(sysfd)
	if e1 != nil {
		return false, e1
	}
	return flags&syscall.FDFLAG_NONBLOCK != 0, nil
}

func HasNonblockFlag(flag int) bool {
	return flag&syscall.FDFLAG_NONBLOCK != 0
}
