// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package runtime

import (
	"internal/runtime/sys"
	"unsafe"
)

// Component model builtins are in internal/wasi/builtins.go.
// The runtime only needs the task-return import below.
//
//go:wasmimport [export]wasi:cli/run@0.3.0-rc-2026-02-09 [task-return]run
func task_return_run(result int32)

// wasip3 clocks — keep in runtime for early bootstrap (nanotime1 runs
// before any other package's init).
//
//go:wasmimport wasi:clocks/monotonic-clock@0.3.0-rc-2026-02-09 now
func monotonic_clock_now() int64

//go:wasmimport wasi:clocks/system-clock@0.3.0-rc-2026-02-09 now
//go:noescape
func system_clock_now(retptr unsafe.Pointer)

//go:wasmimport wasi:clocks/monotonic-clock@0.3.0-rc-2026-02-09 [async-lower]wait-for
func async_wait_for(ns int64) int32

// wasip3 random — keep in runtime for early bootstrap (readRandom runs
// before any other package's init).
//
//go:wasmimport wasi:random/random@0.3.0-rc-2026-02-09 get-random-u64
func random_get_u64() int64

type systemClockInstant struct {
	seconds     int64
	nanoseconds uint32
}

// pausePC pauses execution like pause, but uses the assembler's
// automatic return address handling instead of manually setting SP.
// This works correctly from any call depth, including g0's schedule chain.
// Defined in asm_wasm32.s.
func pausePC()

// usleep is a low-level runtime sleep used by sysmon, freezetheworld, etc.
// On single-threaded wasm, this is a no-op (like js/wasm).
// Goroutine-level async sleep is handled separately through the timer system.
func usleep(usec uint32) {
	// no-op on wasip3, same as js/wasm
}

// wasip3 stderr -- direct CM writes for runtime output (println, panic).
// All runtime output goes to stderr regardless of the fd parameter.

//go:wasmimport wasi:cli/stderr@0.3.0-rc-2026-02-09 [stream-new-0]write-via-stream
func stderr_stream_new() int64

//go:wasmimport wasi:cli/stderr@0.3.0-rc-2026-02-09 write-via-stream
func stderr_write_via_stream(reader int32) int32

//go:wasmimport wasi:cli/stderr@0.3.0-rc-2026-02-09 [async-lower][stream-write-0]write-via-stream
//go:noescape
func stderr_stream_write(writable int32, ptr unsafe.Pointer, n int32) int32

//go:wasmimport wasi:cli/stderr@0.3.0-rc-2026-02-09 [stream-drop-writable-0]write-via-stream
func stderr_stream_drop_writable(writable int32)

//go:wasmimport wasi:cli/stderr@0.3.0-rc-2026-02-09 [future-drop-readable-1]write-via-stream
func stderr_future_drop_readable(readable int32)

func write1(fd uintptr, p unsafe.Pointer, n int32) int32 {
	if n == 0 {
		return 0
	}
	pair := stderr_stream_new()
	reader := int32(pair)
	writer := int32(pair >> 32)
	future := stderr_write_via_stream(reader)
	result := stderr_stream_write(writer, p, n)
	stderr_stream_drop_writable(writer)
	stderr_future_drop_readable(future)
	if result < 0 {
		return 0 // Blocked -- should not happen for stderr
	}
	return result >> 4 // progress count from packed copy result
}

func readRandom(r []byte) int {
	n := 0
	for n+8 <= len(r) {
		v := random_get_u64()
		*(*int64)(unsafe.Pointer(&r[n])) = v
		n += 8
	}
	if n < len(r) {
		v := random_get_u64()
		for i := 0; n < len(r); i++ {
			r[n] = byte(v >> (8 * i))
			n++
		}
	}
	return n
}

func goenvs() {
	argslice = wasip3Args()
	envs = wasip3Envs()
}

//go:linkname wasip3Args syscall.wasip3Args
func wasip3Args() []string

//go:linkname wasip3Envs syscall.wasip3Envs
func wasip3Envs() []string

func walltime() (sec int64, nsec int32) {
	return walltime1()
}

func walltime1() (sec int64, nsec int32) {
	var instant systemClockInstant
	system_clock_now(unsafe.Pointer(&instant))
	return instant.seconds, int32(instant.nanoseconds)
}

func nanotime1() int64 {
	return monotonic_clock_now()
}

// exit signals completion to the host and terminates the component.
// Called by the runtime when main returns or os.Exit is called.
//
//go:linkname exit
func exit(code int32) {
	// Signal the async export result to the host.
	// The WASI result<_, _> type only has ok (0) and err (1) discriminants.
	var result int32
	if code != 0 {
		result = 1
	}
	task_return_run(result)
	// Note: we intentionally do NOT call waitable_set_drop here.
	// Stream/future handles joined via waitable_join are not removed by
	// their typed drop functions (stream-drop-*, future-drop-*), leaving
	// them as "children" that prevent waitable_set_drop from succeeding.
	// The host cleans up all resources when the component terminates.

	// Set callback return to EXIT so the trampoline returns 0 to the host.
	asyncReturnValue = callbackExit
	// Halt the Go scheduler — pause causes wasm_pc_f_loop to exit,
	// and the trampoline returns asyncReturnValue to the host.
	pause(sys.GetCallerSP() - 16)
}
