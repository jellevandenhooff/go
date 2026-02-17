// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
Package wasm implements the assembler for both GOARCH=wasm and
GOARCH=wasm32 WebAssembly targets.

# Architecture overview

Go has two WebAssembly architectures targeting the WebAssembly Core
Specification:

	wasm:   PtrSize=8, RegSize=8
	wasm32: PtrSize=4, RegSize=8

The original GOARCH=wasm port was introduced in 2018 alongside GOOS=js.
It uses 64-bit pointers in anticipation of the WebAssembly memory64
proposal. Since WebAssembly runtimes use 32-bit linear memory addressing,
addresses are truncated to 32 bits at memory operations.

GOARCH=wasm32 uses 32-bit pointers matching the native WebAssembly
address space. This aligns Go's pointer size with the WASI ABI, allowing
go:wasmimport functions to accept pointer arguments directly rather than
requiring manual conversion through uint32 and unsafe.Pointer. See
https://go.dev/issue/63131 for the proposal and discussion.

Both architectures share SSA lowering rules (Wasm.rules), assembler, and
linker code. The compiler backend cmd/compile/internal/wasm32 delegates
to the wasm backend with an overridden LinkArch. PtrSize-dependent
behavior is handled via config.PtrSize checks in the rules.

# Register size and 64-bit operations

WebAssembly locals and stack values are 64-bit. Go's wasm32 port sets
RegSize=8 to match, so 64-bit integer operations (int64, uint64) use
native i64 instructions without decomposition into 32-bit pairs. This
follows a suggestion from the Go compiler team during review of the
original wasm32 prototype (CL 570835).

On wasm32, int64 values have 8-byte alignment as determined by RegSize.
The compiler rounds the argument-to-result boundary to RegSize
(see types/size.go), and this matches the WASI ABI requirement that
64-bit values are 8-byte aligned.

# Architecture family

Both wasm and wasm32 belong to the Wasm architecture family
(goarch.ArchFamily == goarch.WASM). Code detecting any WebAssembly
target uses:

  - goarch.IsWasmFamily (compile-time constant, 1 for wasm or wasm32)
  - sys.Arch.Family == sys.Wasm (in compiler/linker code)

goarch.IsWasm is 1 only for GOARCH=wasm. String comparisons like
GOARCH == "wasm" do not match wasm32.

# Stack layout and calling convention

WebAssembly uses a virtual stack machine. The Go runtime maintains a
call stack in linear memory with these properties:

  - StackAlign=8: the stack pointer is 8-byte aligned on both
    wasm and wasm32, matching the WASI ABI requirement.
  - The return address slot is 8 bytes on both architectures.
    On wasm32 this is larger than PtrSize (4) to maintain stack
    alignment and match the linker's encoding of return PCs.
  - Frame sizes in assembly are multiples of StackAlign (8).

# ABI offsets in assembly

The Go compiler rounds the function argument area to RegSize (8 bytes)
before placing results. On wasm32, RegSize (8) differs from PtrSize (4),
so hand-written assembly must use RegSize-based rounding for result
offsets.

For example, IndexByteString(s string, c byte) int has arguments
totaling 9 bytes (string=8 + byte=1). The compiler rounds up to
RoundUp(9, 8) = 16, placing the result at FP+16. The result offset
is RoundUp(totalArgBytes, RegSize), not RoundUp(totalArgBytes, PtrSize).

# Loads and stores

On wasm, all loads and stores use 64-bit WebAssembly operations.
On wasm32, pointer-width values use 32-bit operations
(I64Load32U/I32Store), while 64-bit values (int64, float64) still
use 64-bit operations (I64Load/I64Store).

# Supported platforms

GOARCH=wasm32 supports GOOS=wasip1 only. The js/wasm32 combination is
not supported.
*/
package wasm
