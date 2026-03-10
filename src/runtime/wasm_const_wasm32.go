// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasm32

package runtime

import "internal/abi"

// wasmPCBBits is exported to assembly via go_asm.h as const_wasmPCBBits.
const wasmPCBBits = abi.ArchWasmPCBBits
