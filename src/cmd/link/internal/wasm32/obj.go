// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package wasm32

import (
	"cmd/internal/sys"
	"cmd/link/internal/ld"
	"cmd/link/internal/wasm"
)

func Init() (*sys.Arch, ld.Arch) {
	_, theArch := wasm.Init()
	return sys.ArchWasm32, theArch
}
