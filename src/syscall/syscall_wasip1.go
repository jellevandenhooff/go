// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip1

package syscall

type clockid = uint32

const (
	clockRealtime clockid = iota
	clockMonotonic
	clockProcessCPUTimeID
	clockThreadCPUTimeID
)

//go:wasmimport wasi_snapshot_preview1 clock_time_get
//go:noescape
func clock_time_get(id clockid, precision timestamp, time *timestamp) Errno

func Gettimeofday(tv *Timeval) error {
	var time timestamp
	if errno := clock_time_get(clockRealtime, 1e3, &time); errno != 0 {
		return errno
	}
	tv.setTimestamp(time)
	return nil
}

func SetNonblock(fd int, nonblocking bool) error {
	flags, err := fd_fdstat_get_flags(fd)
	if err != nil {
		return err
	}
	if nonblocking {
		flags |= FDFLAG_NONBLOCK
	} else {
		flags &^= FDFLAG_NONBLOCK
	}
	errno := fd_fdstat_set_flags(int32(fd), flags)
	return errnoErr(errno)
}
