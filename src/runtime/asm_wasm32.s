// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "go_tls.h"
#include "funcdata.h"
#include "textflag.h"

TEXT runtime·rt0_go(SB), NOSPLIT|NOFRAME|TOPFRAME, $0
	// save m->g0 = g0
	MOVW $runtime·g0(SB), runtime·m0+m_g0(SB)
	// save m0 to g0->m
	MOVW $runtime·m0(SB), runtime·g0+g_m(SB)
	// set g to g0
	MOVD $runtime·g0(SB), g
	CALLNORESUME runtime·check(SB)
#ifdef GOOS_js
	CALLNORESUME runtime·args(SB)
#endif
	CALLNORESUME runtime·osinit(SB)
	CALLNORESUME runtime·schedinit(SB)
	MOVW $runtime·mainPC(SB), 0(SP)
	CALLNORESUME runtime·newproc(SB)
	CALL runtime·mstart(SB) // WebAssembly stack will unwind when switching to another goroutine
	UNDEF

TEXT runtime·mstart(SB),NOSPLIT|TOPFRAME,$0
	CALL	runtime·mstart0(SB)
	RET // not reached

DATA  runtime·mainPC+0(SB)/4,$runtime·main(SB)
GLOBL runtime·mainPC(SB),RODATA,$4

// func checkASM() bool
TEXT ·checkASM(SB), NOSPLIT, $0-1
	MOVB $1, ret+0(FP)
	RET

TEXT runtime·gogo(SB), NOSPLIT, $0-4
	MOVW buf+0(FP), R0
	MOVW gobuf_g(R0), R1
	MOVW 0(R1), R2	// make sure g != nil
	MOVD R1, g
	MOVW gobuf_sp(R0), SP

	// Put target PC at -8(SP), wasm_pc_f_loop will pick it up
	// gobuf.pc is uintptr (4 bytes), store as i32 at start of 8-byte slot
	Get SP
	I32Const $8
	I32Sub
	MOVW gobuf_pc(R0), R1 // load PC (uintptr) into R1
	Get R1
	I32WrapI64
	I32Store $0

	MOVW gobuf_ctxt(R0), CTXT
	// clear to help garbage collector
	MOVW $0, gobuf_sp(R0)
	MOVW $0, gobuf_ctxt(R0)

	I32Const $1
	Return

// func mcall(fn func(*g))
// Switch to m->g0's stack, call fn(g).
// Fn must never return. It should gogo(&g->sched)
// to keep running g.
TEXT runtime·mcall(SB), NOSPLIT, $0-4
	// CTXT = fn
	MOVW fn+0(FP), CTXT
	// R1 = g.m
	MOVW g_m(g), R1
	// R2 = g0
	MOVW m_g0(R1), R2

	// save state in g->sched
	MOVW 0(SP), g_sched+gobuf_pc(g)     // caller's PC (4-byte return address)
	MOVW $fn+0(FP), g_sched+gobuf_sp(g) // caller's SP

	// if g == g0 call badmcall
	Get g
	Get R2
	I64Eq
	If
		JMP runtime·badmcall(SB)
	End

	// switch to g0's stack
	I64Load32U (g_sched+gobuf_sp)(R2)
	I64Const $8
	I64Sub
	I32WrapI64
	Set SP

	// set arg to current g
	MOVD g, 0(SP)

	// switch to g0
	MOVD R2, g

	// call fn
	Get CTXT
	I32WrapI64
	I64Load32U $0
	CALL

	Get SP
	I32Const $8
	I32Add
	Set SP

	JMP runtime·badmcall2(SB)

// func systemstack(fn func())
TEXT runtime·systemstack(SB), NOSPLIT, $0-4
	// R0 = fn
	MOVW fn+0(FP), R0
	// R1 = g.m
	MOVW g_m(g), R1
	// R2 = g0
	MOVW m_g0(R1), R2

	// if g == g0
	Get g
	Get R2
	I64Eq
	If
		// no switch:
		MOVD R0, CTXT

		Get CTXT
		I32WrapI64
		I64Load32U $0
		JMP
	End

	// if g != m.curg
	Get g
	I64Load32U m_curg(R1)
	I64Ne
	If
		CALLNORESUME runtime·badsystemstack(SB)
		CALLNORESUME runtime·abort(SB)
	End

	// switch:

	// save state in g->sched. Pretend to
	// be systemstack_switch if the G stack is scanned.
	MOVW $runtime·systemstack_switch(SB), g_sched+gobuf_pc(g)

	MOVW SP, g_sched+gobuf_sp(g)

	// switch to g0
	MOVD R2, g

	// make it look like mstart called systemstack on g0, to stop traceback
	I64Load32U (g_sched+gobuf_sp)(R2)
	I64Const $8
	I64Sub
	Set R3

	MOVW $runtime·mstart(SB), 0(R3)
	MOVD R3, SP

	// call fn
	MOVD R0, CTXT

	Get CTXT
	I32WrapI64
	I64Load32U $0
	CALL

	// switch back to g
	MOVW g_m(g), R1
	MOVW m_curg(R1), R2
	MOVD R2, g
	MOVW g_sched+gobuf_sp(R2), SP
	MOVW $0, g_sched+gobuf_sp(R2)
	RET

TEXT runtime·systemstack_switch(SB), NOSPLIT, $0-0
	RET

TEXT runtime·abort(SB),NOSPLIT|NOFRAME,$0-0
	UNDEF

// AES hashing not implemented for wasm
TEXT runtime·memhash(SB),NOSPLIT|NOFRAME,$0-20
	JMP	runtime·memhashFallback(SB)
TEXT runtime·strhash(SB),NOSPLIT|NOFRAME,$0-12
	JMP	runtime·strhashFallback(SB)
TEXT runtime·memhash32(SB),NOSPLIT|NOFRAME,$0-12
	JMP	runtime·memhash32Fallback(SB)
TEXT runtime·memhash64(SB),NOSPLIT|NOFRAME,$0-12
	JMP	runtime·memhash64Fallback(SB)

TEXT runtime·asminit(SB), NOSPLIT, $0-0
	// No per-thread init.
	RET

TEXT ·publicationBarrier(SB), NOSPLIT, $0-0
	RET

TEXT runtime·procyieldAsm(SB), NOSPLIT, $0-0 // FIXME
	RET

TEXT runtime·breakpoint(SB), NOSPLIT, $0-0
	UNDEF

// func switchToCrashStack0(fn func())
TEXT runtime·switchToCrashStack0(SB), NOSPLIT, $0-4
	MOVW fn+0(FP), CTXT	// context register
	MOVW	g_m(g), R2	// curm

	// set g to gcrash
	MOVD	$runtime·gcrash(SB), g	// g = &gcrash
	MOVW	R2, g_m(g)	// g.m = curm
	MOVW	g, m_g0(R2)	// curm.g0 = g

	// switch to crashstack
	I64Load32U (g_stack+stack_hi)(g)
	I64Const $(-4*8)
	I64Add
	I32WrapI64
	Set SP

	// call target function
	Get CTXT
	I32WrapI64
	I64Load32U $0
	CALL

	// should never return
	CALL	runtime·abort(SB)
	UNDEF

// Called during function prolog when more stack is needed.
//
// The traceback routines see morestack on a g0 as being
// the top of a stack (for example, morestack calling newstack
// calling the scheduler calling newm calling gc), so we must
// record an argument size. For that purpose, it has no arguments.
TEXT runtime·morestack(SB), NOSPLIT, $0-0
	// R1 = g.m
	MOVW g_m(g), R1

	// R2 = g0
	MOVW m_g0(R1), R2

	// Set g->sched to context in f.
	NOP	SP	// tell vet SP changed - stop checking offsets
	MOVW 0(SP), g_sched+gobuf_pc(g)  // return address (4 bytes in 8-byte slot)
	MOVW $8(SP), g_sched+gobuf_sp(g) // f's SP
	MOVW CTXT, g_sched+gobuf_ctxt(g)

	// Cannot grow scheduler stack (m->g0).
	Get g
	Get R2
	I64Eq
	If
		CALLNORESUME runtime·badmorestackg0(SB)
		CALLNORESUME runtime·abort(SB)
	End

	// Cannot grow signal stack (m->gsignal).
	Get g
	I64Load32U m_gsignal(R1)
	I64Eq
	If
		CALLNORESUME runtime·badmorestackgsignal(SB)
		CALLNORESUME runtime·abort(SB)
	End

	// Called from f.
	// Set m->morebuf to f's caller.
	MOVW 8(SP), m_morebuf+gobuf_pc(R1)   // f's caller's return address (4 bytes in 8-byte slot)
	MOVW $16(SP), m_morebuf+gobuf_sp(R1) // f's caller's SP
	MOVW g, m_morebuf+gobuf_g(R1)

	// Call newstack on m->g0's stack.
	MOVD R2, g
	MOVW g_sched+gobuf_sp(R2), SP
	CALL runtime·newstack(SB)
	UNDEF // crash if newstack returns

// morestack but not preserving ctxt.
TEXT runtime·morestack_noctxt(SB),NOSPLIT,$0
	MOVD $0, CTXT
	JMP runtime·morestack(SB)

TEXT ·asmcgocall(SB), NOSPLIT, $0-0
	UNDEF

#define DISPATCH(NAME, MAXSIZE) \
	Get R0; \
	I64Const $MAXSIZE; \
	I64LeU; \
	If; \
		JMP NAME(SB); \
	End

TEXT ·reflectcall(SB), NOSPLIT, $0-28
	I64Load32U fn+4(FP)
	I64Eqz
	If
		CALLNORESUME runtime·sigpanic<ABIInternal>(SB)
	End

	MOVW frameSize+20(FP), R0

	DISPATCH(runtime·call16, 16)
	DISPATCH(runtime·call32, 32)
	DISPATCH(runtime·call64, 64)
	DISPATCH(runtime·call128, 128)
	DISPATCH(runtime·call256, 256)
	DISPATCH(runtime·call512, 512)
	DISPATCH(runtime·call1024, 1024)
	DISPATCH(runtime·call2048, 2048)
	DISPATCH(runtime·call4096, 4096)
	DISPATCH(runtime·call8192, 8192)
	DISPATCH(runtime·call16384, 16384)
	DISPATCH(runtime·call32768, 32768)
	DISPATCH(runtime·call65536, 65536)
	DISPATCH(runtime·call131072, 131072)
	DISPATCH(runtime·call262144, 262144)
	DISPATCH(runtime·call524288, 524288)
	DISPATCH(runtime·call1048576, 1048576)
	DISPATCH(runtime·call2097152, 2097152)
	DISPATCH(runtime·call4194304, 4194304)
	DISPATCH(runtime·call8388608, 8388608)
	DISPATCH(runtime·call16777216, 16777216)
	DISPATCH(runtime·call33554432, 33554432)
	DISPATCH(runtime·call67108864, 67108864)
	DISPATCH(runtime·call134217728, 134217728)
	DISPATCH(runtime·call268435456, 268435456)
	DISPATCH(runtime·call536870912, 536870912)
	DISPATCH(runtime·call1073741824, 1073741824)
	JMP runtime·badreflectcall(SB)

#define CALLFN(NAME, MAXSIZE) \
TEXT NAME(SB), WRAPPER, $MAXSIZE-28; \
	NO_LOCAL_POINTERS; \
	MOVW stackArgsSize+12(FP), R0; \
	\
	Get R0; \
	I64Eqz; \
	Not; \
	If; \
		Get SP; \
		I64Load32U stackArgs+8(FP); \
		I32WrapI64; \
		I64Load32U stackArgsSize+12(FP); \
		I32WrapI64; \
		MemoryCopy; \
	End; \
	\
	MOVW f+4(FP), CTXT; \
	Get CTXT; \
	I32WrapI64; \
	I64Load32U $0; \
	CALL; \
	\
	I64Load32U stackRetOffset+16(FP); \
	Set R0; \
	\
	MOVW stackArgsType+0(FP), RET0; \
	\
	I64Load32U stackArgs+8(FP); \
	Get R0; \
	I64Add; \
	Set RET1; \
	\
	Get SP; \
	I64ExtendI32U; \
	Get R0; \
	I64Add; \
	Set RET2; \
	\
	I64Load32U stackArgsSize+12(FP); \
	Get R0; \
	I64Sub; \
	Set RET3; \
	\
	CALL callRet<>(SB); \
	RET

// callRet copies return values back at the end of call*. This is a
// separate function so it can allocate stack space for the arguments
// to reflectcallmove. It does not follow the Go ABI; it expects its
// arguments in registers.
TEXT callRet<>(SB), NOSPLIT, $24-0
	NO_LOCAL_POINTERS
	MOVW RET0, 0(SP)
	MOVW RET1, 4(SP)
	MOVW RET2, 8(SP)
	MOVW RET3, 12(SP)
	MOVW $0,   16(SP)
	CALL runtime·reflectcallmove(SB)
	RET

CALLFN(·call16, 16)
CALLFN(·call32, 32)
CALLFN(·call64, 64)
CALLFN(·call128, 128)
CALLFN(·call256, 256)
CALLFN(·call512, 512)
CALLFN(·call1024, 1024)
CALLFN(·call2048, 2048)
CALLFN(·call4096, 4096)
CALLFN(·call8192, 8192)
CALLFN(·call16384, 16384)
CALLFN(·call32768, 32768)
CALLFN(·call65536, 65536)
CALLFN(·call131072, 131072)
CALLFN(·call262144, 262144)
CALLFN(·call524288, 524288)
CALLFN(·call1048576, 1048576)
CALLFN(·call2097152, 2097152)
CALLFN(·call4194304, 4194304)
CALLFN(·call8388608, 8388608)
CALLFN(·call16777216, 16777216)
CALLFN(·call33554432, 33554432)
CALLFN(·call67108864, 67108864)
CALLFN(·call134217728, 134217728)
CALLFN(·call268435456, 268435456)
CALLFN(·call536870912, 536870912)
CALLFN(·call1073741824, 1073741824)

TEXT runtime·goexit(SB), NOSPLIT|TOPFRAME, $0-0
	NOP // first PC of goexit is skipped
	CALL runtime·goexit1(SB) // does not return
	UNDEF

TEXT runtime·cgocallback(SB), NOSPLIT, $0-12
	UNDEF

// gcWriteBarrier informs the GC about heap pointer writes.
//
// gcWriteBarrier does NOT follow the Go ABI. It accepts the
// number of bytes of buffer needed as a wasm argument
// (put on the TOS by the caller, lives in local R0 in this body)
// and returns a pointer to the buffer space as a wasm result
// (left on the TOS in this body, appears on the wasm stack
// in the caller).
TEXT gcWriteBarrier<>(SB), NOSPLIT, $0
	Loop
		// R3 = g.m
		MOVW g_m(g), R3
		// R4 = p
		MOVW m_p(R3), R4
		// R5 = wbBuf.next
		MOVW p_wbBuf+wbBuf_next(R4), R5

		// Increment wbBuf.next
		Get R5
		Get R0
		I64Add
		Set R5

		// Is the buffer full?
		Get R5
		I64Load32U (p_wbBuf+wbBuf_end)(R4)
		I64LeU
		If
			// Commit to the larger buffer.
			MOVW R5, p_wbBuf+wbBuf_next(R4)

			// Make return value (the original next position)
			Get R5
			Get R0
			I64Sub

			Return
		End

		// Flush
		CALLNORESUME runtime·wbBufFlush(SB)

		// Retry
		Br $0
	End

TEXT runtime·gcWriteBarrier1<ABIInternal>(SB),NOSPLIT,$0
	I64Const $4	// 1 * PtrSize
	Call	gcWriteBarrier<>(SB)
	Return
TEXT runtime·gcWriteBarrier2<ABIInternal>(SB),NOSPLIT,$0
	I64Const $8	// 2 * PtrSize
	Call	gcWriteBarrier<>(SB)
	Return
TEXT runtime·gcWriteBarrier3<ABIInternal>(SB),NOSPLIT,$0
	I64Const $12	// 3 * PtrSize
	Call	gcWriteBarrier<>(SB)
	Return
TEXT runtime·gcWriteBarrier4<ABIInternal>(SB),NOSPLIT,$0
	I64Const $16	// 4 * PtrSize
	Call	gcWriteBarrier<>(SB)
	Return
TEXT runtime·gcWriteBarrier5<ABIInternal>(SB),NOSPLIT,$0
	I64Const $20	// 5 * PtrSize
	Call	gcWriteBarrier<>(SB)
	Return
TEXT runtime·gcWriteBarrier6<ABIInternal>(SB),NOSPLIT,$0
	I64Const $24	// 6 * PtrSize
	Call	gcWriteBarrier<>(SB)
	Return
TEXT runtime·gcWriteBarrier7<ABIInternal>(SB),NOSPLIT,$0
	I64Const $28	// 7 * PtrSize
	Call	gcWriteBarrier<>(SB)
	Return
TEXT runtime·gcWriteBarrier8<ABIInternal>(SB),NOSPLIT,$0
	I64Const $32	// 8 * PtrSize
	Call	gcWriteBarrier<>(SB)
	Return

TEXT wasm_pc_f_loop(SB),NOSPLIT,$0
// Call the function for the current PC_F. Repeat until PAUSE != 0 indicates pause or exit.
// The WebAssembly stack may unwind, e.g. when switching goroutines.
// The Go stack on the linear memory is then used to jump to the correct functions
// with this loop, without having to restore the full WebAssembly stack.
// It is expected to have a pending call before entering the loop, so check PAUSE first.
//
// wasm32 encoding: PC = PC_F << 5 | offset
// The full 32-bit PC is passed as the PC_B parameter to the callee.
// The callee subtracts its own base PC to recover the resume point index.
	Get PAUSE
	I32Eqz
	If
	loop:
		Loop
			// Load full 32-bit PC from return address slot at -8(SP)
			Get SP
			I32Const $8
			I32Sub
			I32Load $0 // full PC (32-bit)
			Set R0     // save full PC

			// Pass full PC as PC_B parameter (callee subtracts its base)
			Get R0

			// Compute table index: PC >> wasmPCBBits
			Get R0
			I32Const $const_wasmPCBBits
			I32ShrU

			CallIndirect $0
			Drop

			Get PAUSE
			I32Eqz
			BrIf loop
		End
	End

	I32Const $0
	Set PAUSE

	Return

// wasm_pc_f_loop_export is like wasm_pc_f_loop, except that this takes an
// argument (on Wasm stack) that is a PC_F (table index), and the loop stops
// when we return to that function in a normal return (not unwinding).
// This is for handling a wasmexport function when it needs to switch the stack.
TEXT wasm_pc_f_loop_export(SB),NOSPLIT,$0
	Get PAUSE
	I32Eqz
outer:
	If
		// R1 is whether a function return normally (0) or unwinding (1).
		// Start with unwinding.
		I32Const $1
		Set R1
	loop:
		Loop
			// Load full 32-bit PC from return address slot at -8(SP)
			Get SP
			I32Const $8
			I32Sub
			I32Load $0 // full PC
			Tee R2     // save full PC

			// Compute table index: PC >> wasmPCBBits
			I32Const $const_wasmPCBBits
			I32ShrU
			Tee R3     // save table index

			Get R0
			I32Eq
			If // table index == R0 (stop PC_F), we're at the target function
				Get R1
				I32Eqz
				// Break if it is a normal return
				BrIf outer // actually jump to after the corresponding End
			End

			// Pass full PC as PC_B parameter
			Get R2

			// Table index for call_indirect
			Get R3
			CallIndirect $0
			Set R1 // save return/unwinding state for next iteration

			Get PAUSE
			I32Eqz
			BrIf loop
		End
	End

	I32Const $0
	Set PAUSE

	Return

TEXT wasm_export_lib(SB),NOSPLIT,$0
	UNDEF

TEXT runtime·pause(SB), NOSPLIT, $0-4
	MOVW newsp+0(FP), SP
	I32Const $1
	Set PAUSE
	RETUNWIND

// Called if a wasmexport function is called before runtime initialization
TEXT runtime·notInitialized(SB), NOSPLIT, $0
	MOVD $runtime·wasmStack+(m0Stack__size-16-8)(SB), SP
	I32Const $runtime·notInitialized1(SB) // entry PC_B = callee's base PC
	Call runtime·notInitialized1(SB)
	Drop
	I32Const $runtime·abort(SB) // entry PC_B = callee's base PC
	Call runtime·abort(SB)
	UNDEF
