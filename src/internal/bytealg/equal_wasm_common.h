// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// params: a, b, len
// ret: 0/1
TEXT memeqbody<>(SB), NOSPLIT, $0-0
	Get R0
	Get R1
	I64Eq
	If
		I64Const $1
		Return
	End

loop:
	Loop
		Get R2
		I64Eqz
		If
			I64Const $1
			Return
		End

		Get R0
		I32WrapI64
		I64Load8U $0
		Get R1
		I32WrapI64
		I64Load8U $0
		I64Ne
		If
			I64Const $0
			Return
		End

		Get R0
		I64Const $1
		I64Add
		Set R0

		Get R1
		I64Const $1
		I64Add
		Set R1

		Get R2
		I64Const $1
		I64Sub
		Set R2

		Br loop
	End
	UNDEF
