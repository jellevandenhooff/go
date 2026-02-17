// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "textflag.h"

TEXT ·Compare(SB), NOSPLIT, $0-56
	Get SP
	I64Load a_base+0(FP)
	I64Load a_len+8(FP)
	I64Load b_base+24(FP)
	I64Load b_len+32(FP)
	Call cmpbody<>(SB)
	I64Store ret+48(FP)
	RET

TEXT runtime·cmpstring(SB), NOSPLIT, $0-40
	Get SP
	I64Load a_base+0(FP)
	I64Load a_len+8(FP)
	I64Load b_base+16(FP)
	I64Load b_len+24(FP)
	Call cmpbody<>(SB)
	I64Store ret+32(FP)
	RET

#include "compare_wasm_common.h"
