// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3 && goexperiment.wasiexec

package wasi

import "unsafe"

// ExecResult holds the result of executing a child WASI component.
type ExecResult struct {
	ExitCode uint32
}

// Exec runs a child WASI component at the given path with the specified
// arguments, environment variables (KEY=VALUE format), and working directory.
// The child's stdout and stderr are written to the specified file paths.
// It blocks until the child completes and returns the exit code.
func Exec(path string, args []string, env []string, cwd string, stdoutPath string, stderrPath string) (ExecResult, error) {
	// Build the flat params struct for the canonical ABI.
	//
	// Layout (all i32):
	//   [0]  path ptr
	//   [4]  path len
	//   [8]  args list ptr
	//   [12] args list len
	//   [16] env list ptr
	//   [20] env list len
	//   [24] cwd option discriminant (0=none, 1=some)
	//   [28] cwd ptr
	//   [32] cwd len
	//   [36] stdout-path ptr
	//   [40] stdout-path len
	//   [44] stderr-path ptr
	//   [48] stderr-path len
	//
	// Total: 52 bytes
	var params struct {
		_ [0]int64
		b [52]byte
	}

	// path
	pathPtr, pathLen := StringToPtr(path)
	*(*int32)(unsafe.Pointer(&params.b[0])) = pathPtr
	*(*int32)(unsafe.Pointer(&params.b[4])) = pathLen

	// args: list<string> — array of (ptr, len) pairs
	type stringPair struct {
		ptr int32
		len int32
	}
	argPairs := make([]stringPair, len(args))
	for i, a := range args {
		p, l := StringToPtr(a)
		argPairs[i] = stringPair{p, l}
	}
	if len(argPairs) > 0 {
		*(*int32)(unsafe.Pointer(&params.b[8])) = int32(uintptr(unsafe.Pointer(&argPairs[0])))
	}
	*(*int32)(unsafe.Pointer(&params.b[12])) = int32(len(argPairs))

	// env: list<string> — same layout as args
	envPairs := make([]stringPair, len(env))
	for i, e := range env {
		p, l := StringToPtr(e)
		envPairs[i] = stringPair{p, l}
	}
	if len(envPairs) > 0 {
		*(*int32)(unsafe.Pointer(&params.b[16])) = int32(uintptr(unsafe.Pointer(&envPairs[0])))
	}
	*(*int32)(unsafe.Pointer(&params.b[20])) = int32(len(envPairs))

	// cwd: option<string>
	if cwd != "" {
		*(*int32)(unsafe.Pointer(&params.b[24])) = 1 // some
		cwdPtr, cwdLen := StringToPtr(cwd)
		*(*int32)(unsafe.Pointer(&params.b[28])) = cwdPtr
		*(*int32)(unsafe.Pointer(&params.b[32])) = cwdLen
	}

	// stdout-path
	stdoutPathPtr, stdoutPathLen := StringToPtr(stdoutPath)
	*(*int32)(unsafe.Pointer(&params.b[36])) = stdoutPathPtr
	*(*int32)(unsafe.Pointer(&params.b[40])) = stdoutPathLen

	// stderr-path
	stderrPathPtr, stderrPathLen := StringToPtr(stderrPath)
	*(*int32)(unsafe.Pointer(&params.b[44])) = stderrPathPtr
	*(*int32)(unsafe.Pointer(&params.b[48])) = stderrPathLen

	// Result area for result<u32, string>:
	//   [0]  discriminant (0=ok, 1=err)
	//   ok case:
	//     [4]  exit-code (u32)
	//   err case:
	//     [4]  error string ptr
	//     [8]  error string len
	//
	// Total: 12 bytes
	resultPtr := AllocResult(12)

	status := execWasm(unsafe.Pointer(&params), resultPtr)
	AsyncWait(status)

	// Keep Go references alive across the async suspension
	KeepAlive(path)
	KeepAlive(args)
	KeepAlive(env)
	KeepAlive(cwd)
	KeepAlive(stdoutPath)
	KeepAlive(stderrPath)
	KeepAlive(argPairs)
	KeepAlive(envPairs)

	disc := *(*int32)(resultPtr)
	if disc == 1 {
		// Error case
		errPtr := *(*int32)(unsafe.Pointer(uintptr(resultPtr) + 4))
		errLen := *(*int32)(unsafe.Pointer(uintptr(resultPtr) + 8))
		errStr := CopyString(errPtr, errLen)
		FreeResult(resultPtr)
		return ExecResult{}, errorString(errStr)
	}

	// OK case
	exitCode := *(*uint32)(unsafe.Pointer(uintptr(resultPtr) + 4))
	FreeResult(resultPtr)
	return ExecResult{ExitCode: exitCode}, nil
}

// errorString is a simple error type to avoid importing "errors".
type errorString string

func (e errorString) Error() string { return string(e) }

//go:wasmimport wasi:exec/exec@0.1.0 [async-lower]exec
func execWasm(params unsafe.Pointer, retptr unsafe.Pointer) int32
