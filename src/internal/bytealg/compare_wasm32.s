// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "textflag.h"

TEXT ·Compare(SB), NOSPLIT, $0-28
	Get SP
	I64Load32U a_base+0(FP)
	I64Load32U a_len+4(FP)
	I64Load32U b_base+12(FP)
	I64Load32U b_len+16(FP)
	Call cmpbody<>(SB)
	I64Store32 ret+24(FP)
	RET

TEXT runtime·cmpstring(SB), NOSPLIT, $0-20
	Get SP
	I64Load32U a_base+0(FP)
	I64Load32U a_len+4(FP)
	I64Load32U b_base+8(FP)
	I64Load32U b_len+12(FP)
	Call cmpbody<>(SB)
	I64Store32 ret+16(FP)
	RET

#include "compare_wasm_common.h"
