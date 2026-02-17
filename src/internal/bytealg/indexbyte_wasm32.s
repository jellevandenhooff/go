// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "textflag.h"

TEXT ·IndexByte(SB), NOSPLIT, $0-20
	I64Load32U b_base+0(FP)
	I32WrapI64
	I32Load8U c+12(FP)
	I64Load32U b_len+4(FP)
	I32WrapI64
	Call memchr<>(SB)
	I64ExtendI32U
	Set R0

	Get SP
	I64Const $-1
	Get R0
	I64Load32U b_base+0(FP)
	I64Sub
	Get R0
	I64Eqz $0
	Select
	I64Store32 ret+16(FP)

	RET

TEXT ·IndexByteString(SB), NOSPLIT, $0-20
	Get SP
	I64Load32U s_base+0(FP)
	I32WrapI64
	I32Load8U c+8(FP)
	I64Load32U s_len+4(FP)
	I32WrapI64
	Call memchr<>(SB)
	I64ExtendI32U
	Set R0

	I64Const $-1
	Get R0
	I64Load32U s_base+0(FP)
	I64Sub
	Get R0
	I64Eqz $0
	Select
	I64Store32 ret+16(FP)

	RET

#include "indexbyte_wasm_common.h"
