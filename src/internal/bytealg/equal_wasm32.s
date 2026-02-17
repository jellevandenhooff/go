// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "textflag.h"

// memequal(p, q unsafe.Pointer, size uintptr) bool
TEXT runtime·memequal(SB), NOSPLIT, $0-17
	Get SP
	I64Load32U a+0(FP)
	I64Load32U b+4(FP)
	I64Load32U size+8(FP)
	Call memeqbody<>(SB)
	I64Store8 ret+16(FP)
	RET

// memequal_varlen(a, b unsafe.Pointer) bool
TEXT runtime·memequal_varlen(SB), NOSPLIT, $0-9
	Get SP
	I64Load32U a+0(FP)
	I64Load32U b+4(FP)
	I64Load32U 4(CTXT) // compiler stores size at offset 4 in the closure (PtrSize=4)
	Call memeqbody<>(SB)
	I64Store8 ret+8(FP)
	RET

#include "equal_wasm_common.h"
