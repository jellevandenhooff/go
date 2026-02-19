// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package wasi

import "unsafe"

// Component model builtins (provided by component model runtime).

//go:wasmimport $root [waitable-set-new]
func WaitableSetNew() int32

//go:wasmimport $root [waitable-set-drop]
func WaitableSetDrop(set int32)

//go:wasmimport $root [waitable-set-poll]
//go:noescape
func WaitableSetPoll(set int32, ptr unsafe.Pointer) int32

//go:wasmimport $root [waitable-join]
func WaitableJoin(waitable int32, set int32)

//go:wasmimport $root [async-lower][subtask-cancel]
func SubtaskCancelAsync(handle int32) int32

//go:wasmimport $root [subtask-drop]
func SubtaskDrop(handle int32)

// Context-local storage for the current async task. These are used by
// the component model runtime to associate per-task state. Go doesn't
// use them directly but they must be imported so that jco (the JS
// component model transpiler) emits its AsyncSubtask class definition.

//go:wasmimport $root [context-get-0]
func ContextGet0() int32

//go:wasmimport $root [context-set-0]
func ContextSet0(value int32)
