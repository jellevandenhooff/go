// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build js && wasm

package js_test

import (
	"syscall/js"
	"testing"
)

// TestLargeIntConversion tests int conversion for values that exceed
// 32 bits. These are only valid on wasm where int is 64 bits.
func TestLargeIntConversion(t *testing.T) {
	for _, want := range []int{
		1 << 40, -1 << 40,
		1 << 60, -1 << 60,
	} {
		if got := js.ValueOf(want).Int(); got != want {
			t.Errorf("got %#v, want %#v", got, want)
		}
	}
}
