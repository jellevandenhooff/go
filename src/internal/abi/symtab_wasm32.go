// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasm32

package abi

// ArchWasmPCBBits is the number of bits used for PC_B (resume point offset)
// in the wasm32 code pointer encoding: PC = PC_F << ArchWasmPCBBits | PC_B.
// Functions with more than 1<<ArchWasmPCBBits resume points get multiple
// consecutive entries in the WebAssembly function table, all pointing to the
// same underlying function.
const ArchWasmPCBBits = 5
