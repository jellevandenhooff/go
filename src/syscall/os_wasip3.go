// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package syscall

import (
	"internal/wasi/generated/cli"
	"internal/wasi/generated/clocks"
	"internal/wasi/generated/random"
	"unsafe"
)

func ProcExit(code int32) {
	runtime_exit(code)
}

//go:linkname runtime_exit runtime.exit
func runtime_exit(code int32)

func RandomGet(b []byte) error {
	for len(b) >= 8 {
		v := random.GetRandomU64()
		*(*int64)(unsafe.Pointer(&b[0])) = v
		b = b[8:]
	}
	if len(b) > 0 {
		v := random.GetRandomU64()
		var tmp [8]byte
		*(*int64)(unsafe.Pointer(&tmp[0])) = v
		copy(b, tmp[:])
	}
	return nil
}

func Gettimeofday(tv *Timeval) error {
	instant := clocks.SystemNow()
	tv.Sec = instant.Seconds
	tv.Usec = int64(instant.Nanoseconds) / 1e3
	return nil
}

// SetNonblock is a no-op for wasip3.
func SetNonblock(fdNum int, nonblocking bool) error {
	return nil
}

// wasip3Args returns the program arguments from wasi:cli/environment.
//
//go:linkname wasip3Args
func wasip3Args() []string {
	return cli.GetArguments()
}

// wasip3Envs returns the environment variables from wasi:cli/environment.
//
//go:linkname wasip3Envs
func wasip3Envs() []string {
	entries := cli.GetEnvironment()
	envs := make([]string, len(entries))
	for i, e := range entries {
		envs[i] = e.F0 + "=" + e.F1
	}
	return envs
}
