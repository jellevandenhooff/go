// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "textflag.h"
#include "funcdata.h"

// makeFuncStub is the code half of the function returned by MakeFunc.
// See the comment on the declaration of makeFuncStub in makefunc.go
// for more details.
// No arg size here; runtime pulls arg map out of the func value.
//
// Frame layout (PtrSize=4, padded to 24 for StackAlign=8):
//   0(SP): ctxt *makeFuncImpl       (4 bytes)
//   4(SP): frame unsafe.Pointer     (4 bytes)
//   8(SP): retValid *bool           (4 bytes)
//  12(SP): regs *abi.RegArgs        (4 bytes)
//  16(SP): retValid bool (local)    (1 byte)
//  17-23:  padding
TEXT ·makeFuncStub(SB),(NOSPLIT|WRAPPER),$24
	NO_LOCAL_POINTERS

	MOVW CTXT, 0(SP)

	Get SP
	Get SP
	I64ExtendI32U
	I64Const $argframe+0(FP)
	I64Add
	I64Store32 $4

	MOVB $0, 16(SP)
	MOVW $16(SP), 8(SP)
	MOVW $0, 12(SP)

	CALL ·callReflect(SB)
	RET

// methodValueCall is the code half of the function returned by makeMethodValue.
// See the comment on the declaration of methodValueCall in makefunc.go
// for more details.
// No arg size here; runtime pulls arg map out of the func value.
TEXT ·methodValueCall(SB),(NOSPLIT|WRAPPER),$24
	NO_LOCAL_POINTERS

	MOVW CTXT, 0(SP)

	Get SP
	Get SP
	I64ExtendI32U
	I64Const $argframe+0(FP)
	I64Add
	I64Store32 $4

	MOVB $0, 16(SP)
	MOVW $16(SP), 8(SP)
	MOVW $0, 12(SP)

	CALL ·callMethod(SB)
	RET
