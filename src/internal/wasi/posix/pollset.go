// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package posix

import _ "unsafe" // for go:linkname

// Poll error constants matching internal/poll.
const (
	pollNoError    = 0
	pollErrClosing = 1
	pollErrTimeout = 2
)

// RuntimePollWait blocks the goroutine on the runtime poller until the
// pollDesc is ready for the given mode ('r' or 'w'). Returns a poll error
// constant. Provided by runtime via //go:linkname.
//
//go:linkname RuntimePollWait
func RuntimePollWait(ctx uintptr, mode int) int

// Extern declarations for runtime-provided functions.
// The runtime provides implementations via //go:linkname on its side.
// This follows the same pattern as internal/poll.runtime_pollOpen.

// PollsetArm registers a stream handle in the handle table and joins it
// to the waitable set. Returns the handle for PollsetDisarm.
//
//go:linkname PollsetArm
func PollsetArm(handle int32, ctx uintptr, mode int32) int32

// PollsetDisarm reads the result for a handle, removes the handle from
// the waitable set, and clears the entry.
//
//go:linkname PollsetDisarm
func PollsetDisarm(handle int32) int32

// SubtaskCancelAndWait cancels a pending subtask and blocks until
// cancellation completes.
//
//go:linkname SubtaskCancelAndWait
func SubtaskCancelAndWait(handle int32)

// PollsetPeekResult reads the event result for a handle without
// disarming. Used to check if a subtask completed before cancelling.
//
//go:linkname PollsetPeekResult
func PollsetPeekResult(handle int32) int32
