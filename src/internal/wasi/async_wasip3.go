// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package wasi

import _ "unsafe" // for go:linkname

// AsyncWait blocks until an async-lowered call completes.
// If status indicates synchronous completion (StatusReturn), it returns immediately.
//
//go:nosplit
func AsyncWait(status int32) {
	if !IsReturned(status) {
		waitForSubtask(Subtask(status))
	}
}

// WaitForSubtask parks the calling goroutine until the given subtask completes.
// Called after an [async-lower] wasi import returns a pending subtask.
func WaitForSubtask(subtask int32) {
	waitForSubtask(subtask)
}

// WaitForWaitable parks the calling goroutine until the given waitable
// (stream end, future end) fires. Returns the event result from the callback.
func WaitForWaitable(handle int32) int32 {
	return waitForWaitable(handle)
}

// waitForSubtask is provided by the runtime via //go:linkname.
// It parks the calling goroutine until the subtask completes, then drops it.
//
//go:linkname waitForSubtask
func waitForSubtask(subtask int32)

// waitForWaitable is provided by the runtime via //go:linkname.
// It parks the calling goroutine until the waitable fires.
//
//go:linkname waitForWaitable
func waitForWaitable(handle int32) int32
