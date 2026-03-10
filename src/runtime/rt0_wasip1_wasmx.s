// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasm || wasm32

#include "go_asm.h"
#include "textflag.h"

#ifdef GOARCH_wasm32
TEXT _rt0_wasm32_wasip1(SB),NOSPLIT,$0
#else
TEXT _rt0_wasm_wasip1(SB),NOSPLIT,$0
#endif
	MOVD $runtime·wasmStack+(m0Stack__size-16)(SB), SP

#ifdef GOARCH_wasm32
	I32Const $runtime·rt0_go(SB)
#else
	I32Const $0 // entry PC_B
#endif
	Call runtime·rt0_go(SB)
	Drop
	Call wasm_pc_f_loop(SB)

	Return

#ifdef GOARCH_wasm32
TEXT _rt0_wasm32_wasip1_lib(SB),NOSPLIT,$0
	Call _rt0_wasm32_wasip1(SB)
#else
TEXT _rt0_wasm_wasip1_lib(SB),NOSPLIT,$0
	Call _rt0_wasm_wasip1(SB)
#endif
	Return
