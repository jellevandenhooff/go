// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"reflect"
	"runtime"
	"testing"
)

// Signal size changes of important structures.

func TestSizeof(t *testing.T) {
	const _64bit = ^uint(0)>>32 != 0

	var tests = []struct {
		val     any     // type as a value
		_32bit  uintptr // size on 32bit platforms
		_64bit  uintptr // size on 64bit platforms
		_wasm32 uintptr // size on wasm32 (0 means same as _32bit)
	}{
		// Types
		{Basic{}, 16, 32, 0},
		{Array{}, 16, 24, 0},
		{Slice{}, 8, 16, 0},
		{Struct{}, 24, 48, 0},
		{Pointer{}, 8, 16, 0},
		{Tuple{}, 12, 24, 0},
		{Signature{}, 28, 56, 0},
		{Union{}, 12, 24, 0},
		{Interface{}, 40, 80, 0},
		{Map{}, 16, 32, 0},
		{Chan{}, 12, 24, 0},
		{Named{}, 68, 128, 0},
		{TypeParam{}, 28, 48, 32},
		{term{}, 12, 24, 0},

		// Objects
		{PkgName{}, 56, 96, 0},
		{Const{}, 60, 104, 0},
		{TypeName{}, 52, 88, 0},
		{Var{}, 60, 104, 0},
		{Func{}, 60, 104, 0},
		{Label{}, 56, 96, 0},
		{Builtin{}, 56, 96, 0},
		{Nil{}, 52, 88, 0},

		// Misc
		{Scope{}, 60, 104, 0},
		{Package{}, 44, 88, 0},
		{_TypeSet{}, 28, 56, 0},
	}

	for _, test := range tests {
		got := reflect.TypeOf(test.val).Size()
		want := test._32bit
		if _64bit {
			want = test._64bit
		}
		if runtime.GOARCH == "wasm32" && test._wasm32 != 0 {
			want = test._wasm32
		}
		if got != want {
			t.Errorf("unsafe.Sizeof(%T) = %d, want %d", test.val, got, want)
		}
	}
}
