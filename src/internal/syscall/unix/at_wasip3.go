// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package unix

import (
	"syscall"
)

const (
	// UTIME_OMIT is the sentinel value to indicate that a time value should not
	// be changed. Its value must match syscall/fs_wasip3.go
	UTIME_OMIT = -0x2

	AT_REMOVEDIR        = 0x200
	AT_SYMLINK_NOFOLLOW = 0x100
)

func Unlinkat(dirfd int, path string, flags int) error {
	return syscall.Unlinkat(dirfd, path, flags)
}

func Openat(dirfd int, path string, flags int, perm uint32) (int, error) {
	return syscall.Openat(dirfd, path, flags, perm)
}

func Fstatat(dirfd int, path string, stat *syscall.Stat_t, flags int) error {
	return syscall.Fstatat(dirfd, path, stat, flags)
}

func Readlinkat(dirfd int, path string, buf []byte) (int, error) {
	return syscall.Readlinkat(dirfd, path, buf)
}

func Mkdirat(dirfd int, path string, mode uint32) error {
	return syscall.Mkdirat(dirfd, path, mode)
}

func Fchmodat(dirfd int, path string, mode uint32, flags int) error {
	return syscall.ENOSYS
}

func Fchownat(dirfd int, path string, uid, gid int, flags int) error {
	return syscall.ENOSYS
}

func Renameat(olddirfd int, oldpath string, newdirfd int, newpath string) error {
	return syscall.Renameat(olddirfd, oldpath, newdirfd, newpath)
}

func Linkat(olddirfd int, oldpath string, newdirfd int, newpath string, flag int) error {
	return syscall.Linkat(olddirfd, oldpath, newdirfd, newpath, flag)
}

func Symlinkat(oldpath string, newdirfd int, newpath string) error {
	return syscall.Symlinkat(oldpath, newdirfd, newpath)
}
