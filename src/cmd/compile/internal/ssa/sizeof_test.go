// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa

import (
	"reflect"
	"runtime"
	"testing"
	"unsafe"
)

// Assert that the size of important structures do not change unexpectedly.

func TestSizeof(t *testing.T) {
	const _64bit = unsafe.Sizeof(uintptr(0)) == 8

	var tests = []struct {
		val     any     // type as a value
		_32bit  uintptr // size on 32bit platforms
		_64bit  uintptr // size on 64bit platforms
		_wasm32 uintptr // size on wasm32 (0 means same as _32bit)
	}{
		{Value{}, 72, 112, 80},
		{Block{}, 168, 312, 176},
		{LocalSlot{}, 28, 40, 32},
		{valState{}, 28, 40, 32},
	}

	for _, tt := range tests {
		want := tt._32bit
		if _64bit {
			want = tt._64bit
		}
		if runtime.GOARCH == "wasm32" && tt._wasm32 != 0 {
			want = tt._wasm32
		}
		got := reflect.TypeOf(tt.val).Size()
		if want != got {
			t.Errorf("unsafe.Sizeof(%T) = %d, want %d", tt.val, got, want)
		}
	}
}
