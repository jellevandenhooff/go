// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "textflag.h"

// _rt0_wasm32_wasip3 is the async entry point for the wasip3 component.
// It is exported as [async-lift]wasi:cli/run@0.3.0-rc-2026-02-09#run.
// It initializes the Go runtime, starts the scheduler, and when all
// goroutines are blocked on async operations, returns an i32 encoding
// WAIT|(waitable_set<<4) so the host knows which waitable set to poll.
TEXT _rt0_wasm32_wasip3(SB),NOSPLIT,$0
	// Reference the callback function to prevent dead-code elimination.
	I32Const $wasm_export_asyncRunCallback(SB)
	Drop

	MOVD $runtime·wasmStack+(m0Stack__size-16)(SB), SP

	I32Const $0 // entry PC_B
	Call runtime·rt0_go(SB)
	Drop
	Call wasm_pc_f_loop(SB)

	// Protect the resume PC at SP-8 from being overwritten by wasmexport
	// calls (e.g. cabi_realloc) during async processing. The host may call
	// cabi_realloc between returning WAIT and the next callback. Its
	// wasmexport wrapper allocates a frame starting at SP and growing down,
	// which would overwrite the resume PC stored at SP-8. Save SP and lower
	// it so the wrapper's frame lands safely below the resume PC slot.
	I32Const $runtime·wasmExportSP(SB)
	Get SP
	I32Store $0
	Get SP
	I32Const $16
	I32Sub
	Set SP

	// Return asyncReturnValue to host.
	I32Const $runtime·asyncReturnValue(SB)
	I32Load $0
	Return

// wasm_export_asyncRunCallback is the callback entry point for the wasip3
// component. It is exported as
// [callback][async-lift]wasi:cli/run@0.3.0-rc-2026-02-09#run.
// The host calls this when a waitable set event fires.
// Parameters: R0=event, R1=p1 (subtask handle), R2=p2 (status).
// Returns an i32: either WAIT|(set<<4) to keep waiting, or EXIT(0) when done.
TEXT wasm_export_asyncRunCallback(SB),NOSPLIT,$0
	// Restore SP to the position saved before returning to the host.
	// This undoes the lowering applied to protect the resume PC at SP-8.
	I32Const $runtime·wasmExportSP(SB)
	I32Load $0
	Set SP

	// Store callback parameters in globals for netpoll to read after pause.
	I32Const $runtime·asyncEventCode(SB)
	Get R0
	I32Store $0

	I32Const $runtime·asyncEventP1(SB)
	Get R1
	I32Store $0

	I32Const $runtime·asyncEventP2(SB)
	Get R2
	I32Store $0

	// Resume Go scheduler if we're still waiting (not exited).
	// asyncReturnValue is 0 (EXIT) when the component has finished, or
	// non-zero (WAIT|set<<4) when netpoll is paused waiting for events.
	// If the host fires a callback after EXIT, skip wasm_pc_f_loop.
	I32Const $runtime·asyncReturnValue(SB)
	I32Load $0
	If
		Call wasm_pc_f_loop(SB)
	End

	// Protect the resume PC again before returning to the host.
	I32Const $runtime·wasmExportSP(SB)
	Get SP
	I32Store $0
	Get SP
	I32Const $16
	I32Sub
	Set SP

	// Return asyncReturnValue to host.
	I32Const $runtime·asyncReturnValue(SB)
	I32Load $0
	Return
