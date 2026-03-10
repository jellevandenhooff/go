// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasm

package abi

// ArchWasmPCBBits is the number of bits used for PC_B (resume point offset)
// in the wasm code pointer encoding: PC = PC_F << ArchWasmPCBBits | PC_B.
const ArchWasmPCBBits = 16
