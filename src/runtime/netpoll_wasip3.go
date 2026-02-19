// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package runtime

import (
	"internal/wasi"
	"unsafe"
)

// WASI preview 3 network poller and async event processor.
//
// Unlike traditional netpoll implementations that use epoll/kqueue/poll_oneoff,
// wasip3 uses the Component Model's waitable set mechanism.
//
// On epoll/kqueue, the kernel stores a pollDesc pointer in the event data and
// returns it directly — no lookup table needed. CM callbacks only pass
// (event_type, handle, result), so we need a handle→pollDesc mapping.
//
// The handle table is a flat array indexed by CM handle ID. All CM handle
// types share a single per-instance table (per the spec) with free-list
// reuse, so IDs stay dense and small. This gives O(1) lookup for all
// operations. Each entry maps a handle to either a pollDesc (network I/O)
// or a parked goroutine (async subtask/stream/future).
//
// Arming happens in the I/O path (internal/poll.awaitStream) via pollsetArm
// when stream-read-0 or stream-write-0 returns Blocked. netpollarm is unused.
//
// Blocking happens in netpoll(delay) via pausePC(), which yields control to
// the host. When a callback fires, the trampoline stores event parameters in
// globals and calls wasm_pc_f_loop, which resumes from the pause point.
// netpoll then reads the event globals and processes the event, returning
// woken goroutines.

// Callback return codes. Set in asyncReturnValue before pausePC().
const (
	callbackExit = 0
	callbackWait = 2
)

// asyncReturnValue is set before calling pausePC(). The callback trampoline
// reads this after wasm_pc_f_loop exits and returns it to the host.
var asyncReturnValue int32

// Globals written by the callback trampoline, read by netpoll after pausePC.
// Safe without synchronization: wasm is single-threaded and the component model
// serializes callbacks — the host calls wasm_export_asyncRunCallback, which
// stores params here and calls wasm_pc_f_loop to resume netpoll. No second
// callback can fire until the first returns to the host.
var (
	asyncEventCode int32
	asyncEventP1   int32
	asyncEventP2   int32
)

// Component model event types (first callback argument).
const (
	eventSubtask     = 1
	eventStreamRead  = 2
	eventStreamWrite = 3
	eventFutureRead  = 4
	eventFutureWrite = 5
)

// Component model status codes and handle encoding.
const (
	statusMask   = 0xf
	handleShift  = 4
	statusReturn = 2
	blocked      = -1 // 0xffffffff, async cancel returned BLOCKED
)

// --- Handle table ---
//
// A flat array indexed by CM handle ID that maps handles to pollDescs
// (network I/O) and parked goroutines (async subtasks/streams/futures).
//
// The component-model spec defines a single Table per component instance
// for all handle types (resources, waitables, streams, futures, subtasks,
// error contexts). It uses a free list for reuse, so IDs stay dense.
// This lets us index directly by handle — O(1) for all operations.
//
// All fields are non-pointer types (uintptr, int32, bool) so that
// operations are safe in nosplit/nowritebarrierrec contexts.
//
// Each entry can be:
//   - A pollset entry: pd != 0 (network socket stream, armed for I/O)
//   - An async waiter: gp != 0 or drop == true (parked goroutine)
//   - Empty: all fields zero
//
// The result field captures event results regardless of entry state.
// This lets checkStreamDropped detect async events (e.g., CopyDropped)
// that arrive between arm/disarm cycles when no goroutine is waiting.

const maxHandles = 4096

type handleEntry struct {
	pd      uintptr // *pollDesc as uintptr (pollset entries)
	mode    int32   // 'r' or 'w' (pollset entries)
	result  int32   // event result from callback
	gp      uintptr // *g as uintptr (async waiters)
	drop    bool    // drop handle on wake (subtasks)
	evicted bool    // netpollclose cleared this entry; result preserved
}

var (
	handles       [maxHandles]handleEntry
	handleCount   int32             // number of active entries
	handleResults [maxHandles]int32 // event results for async waiters (int32, no write barrier)
)

// --- fd→handle secondary index ---
//
// Maps file descriptors to their currently-armed pollset handles.
// Each fd has at most 2 armed handles (read stream + write stream).
// Enables O(1) netpollclose without walking the treap.

const maxFDHandles = 1024

var fdHandles [maxFDHandles][2]int32

// --- Waitable set handle ---

var waitableSet int32 = -1

//go:nosplit
func pollsetInit() {
	if waitableSet == -1 {
		netpollGenericInit()
		waitableSet = wasi.WaitableSetNew()
		// Store the current async task context in context-local slot 0.
		// The component model runtime uses context slots to associate
		// per-task state. On jco (JS transpiler), importing context-get-0
		// and context-set-0 is required for jco to emit its async
		// task class definitions.
		wasi.ContextSet0(wasi.ContextGet0())
	}
}

// wasmExportSP holds the true SP value while it is temporarily lowered
// to protect the resume PC at SP-8 during async processing. Between
// returning WAIT to the host and the next callback, the host may call
// wasmexport functions (e.g. cabi_realloc) whose wrappers allocate frames
// starting at SP. Without protection, the frame overwrites the resume PC.
// See rt0_wasip3_wasm32.s for the save/restore logic.
var wasmExportSP int32

// --- RuntimePollWait ---
//
// Exported to internal/wasi/fd so it can block on the pollDesc directly.

//go:linkname wasifd_RuntimePollWait internal/wasi/posix.RuntimePollWait
func wasifd_RuntimePollWait(ctx uintptr, mode int) int {
	return poll_runtime_pollWait((*pollDesc)(unsafe.Pointer(ctx)), mode)
}

// --- Pollset API ---
//
// Used by internal/poll for network socket streams.

// pollsetArm registers a stream handle in the handle table and joins it
// to the waitable set. Called from internal/poll before pd.waitRead/waitWrite.
// Returns the handle for pollsetDisarm.
//
//go:linkname pollsetArm internal/wasi/posix.PollsetArm
func pollsetArm(handle int32, ctx uintptr, mode int32) int32 {
	pollsetInit()
	if handle <= 0 || handle >= maxHandles {
		throw("pollsetArm: handle out of range")
	}
	if handles[handle].pd != 0 {
		throw("pollsetArm: handle already armed")
	}
	wasi.WaitableJoin(handle, waitableSet)
	handles[handle] = handleEntry{pd: ctx, mode: mode}
	handleCount++
	netpollWaiters.Add(1)

	// Track handle by fd for O(1) netpollclose.
	pd := (*pollDesc)(unsafe.Pointer(ctx))
	if pd.fd >= maxFDHandles {
		throw("pollsetArm: fd out of range")
	}
	if mode == 'r' {
		fdHandles[pd.fd][0] = handle
	} else {
		fdHandles[pd.fd][1] = handle
	}

	return handle
}

// pollsetDisarm reads the result for a handle, removes the handle from
// the waitable set, and clears the entry. Removing from the set prevents
// the host from delivering events for the handle while no goroutine is
// waiting — pollsetArm will re-join when the next operation blocks.
//
//go:linkname pollsetDisarm internal/wasi/posix.PollsetDisarm
func pollsetDisarm(handle int32) int32 {
	if handle <= 0 || handle >= maxHandles {
		return 0
	}
	e := &handles[handle]
	if e.evicted {
		// Removed by netpollclose. Return the preserved result
		// (may be non-zero if the event was delivered before eviction)
		// and clear the entry.
		result := e.result
		handles[handle] = handleEntry{}
		return result
	}
	if e.pd == 0 {
		return 0
	}
	result := e.result
	fd := (*pollDesc)(unsafe.Pointer(e.pd)).fd
	mode := e.mode
	handles[handle] = handleEntry{}
	handleCount--
	netpollWaiters.Add(-1)
	wasi.WaitableJoin(handle, 0) // remove from waitable set

	// Clear fd→handle tracking.
	if fd < maxFDHandles {
		if mode == 'r' {
			fdHandles[fd][0] = 0
		} else {
			fdHandles[fd][1] = 0
		}
	}
	return result
}

// pollsetFindAndDeliver stores an event result for a handle. If the handle
// has an active pollset entry (pd != 0), returns the pollDesc and mode for
// netpollready. Otherwise the result is stashed in the entry so
// checkStreamDropped can detect it before the next stream operation.
//
//go:nosplit
//go:nowritebarrierrec
func pollsetFindAndDeliver(handle int32, result int32) (*pollDesc, int32) {
	if handle <= 0 || handle >= maxHandles {
		return nil, 0
	}
	e := &handles[handle]
	e.result = result
	if e.pd == 0 {
		return nil, 0
	}
	return (*pollDesc)(noescape(unsafe.Pointer(e.pd))), e.mode
}

// pollsetDeleteHandle removes a handle entry and removes the handle
// from the waitable set. Used by netpollclose.
//
// Sets evicted=true and preserves the result field so that callers
// (awaitSubtask, SubtaskCancelAndWait, PollsetDisarm) can detect
// that netpollclose already ran and whether the terminal event was
// delivered. This is critical for subtask handles: SubtaskCancel
// traps if the terminal status was already delivered, so the caller
// must call SubtaskDrop instead.
func pollsetDeleteHandle(handle int32) {
	if handle <= 0 || handle >= maxHandles {
		return
	}
	e := &handles[handle]
	if e.pd != 0 || e.gp != 0 || e.drop {
		result := e.result
		handles[handle] = handleEntry{result: result, evicted: true}
		handleCount--
		netpollWaiters.Add(-1)
		wasi.WaitableJoin(handle, 0) // remove from waitable set
	}
}

// checkStreamDropped checks whether an async event (typically CopyDropped)
// was delivered for a stream handle while no goroutine was waiting.
// Called from syscall.sockRead/sockWrite before invoking host stream
// operations. Returns the event result, or 0 if none pending.
//
//go:nosplit
//go:nowritebarrierrec
//go:linkname checkStreamDropped internal/wasi/posix.checkStreamDropped
func checkStreamDropped(handle int32) int32 {
	if handle <= 0 || handle >= maxHandles {
		return 0
	}
	result := handles[handle].result
	if result != 0 {
		handles[handle].result = 0
	}
	return result
}

// --- Async waiter API ---
//
// Used for subtask, stream, and future events that park a goroutine.

//go:nosplit
//go:nowritebarrierrec
func addAsyncWaiter(handle int32, gp uintptr, drop bool) {
	if handle <= 0 || handle >= maxHandles {
		throw("addAsyncWaiter: handle out of range")
	}
	handles[handle] = handleEntry{gp: gp, drop: drop}
	handleCount++
	netpollWaiters.Add(1)
}

//go:nosplit
//go:nowritebarrierrec
func removeAsyncWaiter(handle int32) (*g, bool) {
	if handle <= 0 || handle >= maxHandles {
		return nil, false
	}
	e := &handles[handle]
	if e.gp == 0 && !e.drop {
		return nil, false
	}
	gp := e.gp
	drop := e.drop
	handles[handle] = handleEntry{}
	handleCount--
	netpollWaiters.Add(-1)
	return (*g)(noescape(unsafe.Pointer(gp))), drop
}

// waitForSubtask parks the calling goroutine until the given subtask completes.
// Called after an [async-lower] wasi import returns a pending subtask.
//
// nosplit so the call chain from blocking async wrappers (which use
// noescape retptrs on the goroutine stack) through AsyncWait to here
// doesn't trigger stack growth that would relocate the retptr.
//
//go:nosplit
//go:linkname waitForSubtask internal/wasi.waitForSubtask
func waitForSubtask(subtask int32) {
	pollsetInit()
	wasi.WaitableJoin(subtask, waitableSet)
	gp := getg()
	addAsyncWaiter(subtask, uintptr(unsafe.Pointer(gp)), true)
	gopark(nil, nil, waitReasonIOWait, traceBlockNet, 1)
	// subtask_drop already called by netpoll event processing
}

// waitForWaitable parks the calling goroutine until the given waitable
// (stream end, future end) fires. Unlike waitForSubtask, this does NOT
// drop the handle — the caller manages the lifecycle.
// Returns the event result (p2) from the callback.
//
//go:linkname waitForWaitable internal/wasi.waitForWaitable
func waitForWaitable(handle int32) int32 {
	pollsetInit()
	wasi.WaitableJoin(handle, waitableSet)
	gp := getg()
	addAsyncWaiter(handle, uintptr(unsafe.Pointer(gp)), false)
	gopark(nil, nil, waitReasonIOWait, traceBlockNet, 1)
	return handleResults[handle]
}

// --- Poll subtask helpers ---

// pollsetPeekResult reads the event result for a handle without
// disarming or removing it from the waitable set. Used by awaitSubtask
// to check if the subtask completed before deciding whether to cancel.
//
//go:linkname pollsetPeekResult internal/wasi/posix.PollsetPeekResult
func pollsetPeekResult(handle int32) int32 {
	if handle <= 0 || handle >= maxHandles {
		return 0
	}
	return handles[handle].result
}

// pollSubtaskCancelAndWait cancels a pending subtask and blocks until
// cancellation completes.
//
// The handle must still be in the waitable set (as a pollset entry from
// pollsetArm). This is critical: removing the handle before cancel
// (via pollsetDisarm) can cause wasmtime to mark the terminal status
// as "delivered", making [async-lower][subtask-cancel] trap.
//
// Uses [async-lower][subtask-cancel] so we don't depend on the host
// blocking internally (which wasmtime happens to do for host subtasks,
// but the CM spec doesn't require). If the async cancel returns BLOCKED,
// we transition the handle from a pollset entry to an async waiter and
// park the goroutine. The event loop will process the terminal event,
// auto-drop the handle (drop=true), and wake the goroutine.
//
// If the cancel completes immediately (non-BLOCKED), we disarm the
// pollset entry and drop the handle ourselves.
//
//go:linkname pollSubtaskCancelAndWait internal/wasi/posix.SubtaskCancelAndWait
func pollSubtaskCancelAndWait(handle int32) {
	if handle <= 0 || handle >= maxHandles {
		return
	}
	e := &handles[handle]
	if e.evicted {
		// netpollclose already cleared the entry and removed the handle
		// from the waitable set. Check the preserved result to determine
		// the subtask state:
		// - result != 0: terminal event delivered → drop
		// - result == 0: still pending → cancel + drop
		result := e.result
		handles[handle] = handleEntry{}
		if result != 0 {
			wasi.SubtaskDrop(handle)
		} else {
			cancelResult := wasi.SubtaskCancelAsync(handle)
			if cancelResult == blocked {
				pollsetInit()
				wasi.WaitableJoin(handle, waitableSet)
				gp := getg()
				addAsyncWaiter(handle, uintptr(unsafe.Pointer(gp)), true)
				gopark(nil, nil, waitReasonIOWait, traceBlockNet, 1)
			} else {
				wasi.SubtaskDrop(handle)
			}
		}
		return
	}
	if e.pd == 0 && e.gp == 0 && !e.drop {
		// Handle never armed in waitable set (e.g., connect subtask
		// created before netpollopen). Use async cancel.
		pollsetInit()
		cancelResult := wasi.SubtaskCancelAsync(handle)
		if cancelResult == blocked {
			wasi.WaitableJoin(handle, waitableSet)
			gp := getg()
			addAsyncWaiter(handle, uintptr(unsafe.Pointer(gp)), true)
			gopark(nil, nil, waitReasonIOWait, traceBlockNet, 1)
		} else {
			wasi.SubtaskDrop(handle)
		}
		return
	}

	result := wasi.SubtaskCancelAsync(handle)
	if result == blocked {
		// Cancel pending — transition from pollset entry to async
		// waiter so the event loop wakes this goroutine on completion.
		pd := (*pollDesc)(unsafe.Pointer(e.pd))
		mode := e.mode

		// Clear fd→handle tracking for the pollset entry.
		if pd.fd < maxFDHandles {
			if mode == 'r' {
				fdHandles[pd.fd][0] = 0
			} else {
				fdHandles[pd.fd][1] = 0
			}
		}

		// Replace pollset entry with async waiter. handleCount and
		// netpollWaiters stay unchanged (replacing, not adding).
		gp := getg()
		handles[handle] = handleEntry{gp: uintptr(unsafe.Pointer(gp)), drop: true}
		gopark(nil, nil, waitReasonIOWait, traceBlockNet, 1)
		return
	}
	// Completed immediately. Disarm the pollset entry and drop.
	pollsetDisarm(handle)
	wasi.SubtaskDrop(handle)
}

// netpollProcessEvent handles a single event from the waitable set.
// Returns the delta for netpollWaiters adjustments via netpollready.
//
//go:nosplit
//go:nowritebarrierrec
func netpollProcessEvent(toRun *gList, event, p1, p2 int32, timerHandle *int32) int32 {
	var delta int32
	switch event {
	case eventSubtask:
		status := p2 & statusMask
		if status >= statusReturn {
			if pd, mode := pollsetFindAndDeliver(p1, p2); pd != nil {
				delta += netpollready(toRun, pd, mode)
			} else {
				handleResults[p1] = p2
				gp, drop := removeAsyncWaiter(p1)
				if drop {
					wasi.SubtaskDrop(p1)
				}
				if gp != nil {
					toRun.push(gp)
				}
			}
			if p1 == *timerHandle {
				*timerHandle = -1
			}
		}
	case eventStreamRead, eventStreamWrite, eventFutureRead, eventFutureWrite:
		if pd, mode := pollsetFindAndDeliver(p1, p2); pd != nil {
			delta += netpollready(toRun, pd, mode)
		} else {
			handleResults[p1] = p2
			gp, drop := removeAsyncWaiter(p1)
			if drop {
				wasi.SubtaskDrop(p1)
			}
			if gp != nil {
				toRun.push(gp)
			}
		}
	default:
		throw("netpoll: unknown event type")
	}
	return delta
}

// Standard netpoll interface.

func netpollinit() {}

func netpollIsPollDescriptor(fd uintptr) bool { return false }

//go:linkname wasifd_SetRuntimeCtx internal/wasi/posix.SetRuntimeCtx
func wasifd_SetRuntimeCtx(fd int, ctx uintptr)

//go:linkname wasifd_ArmPendingConnect internal/wasi/posix.ArmPendingConnect
func wasifd_ArmPendingConnect(fd int)

func netpollopen(fd uintptr, pd *pollDesc) int32 {
	wasifd_SetRuntimeCtx(int(fd), uintptr(unsafe.Pointer(pd)))
	wasifd_ArmPendingConnect(int(fd)) // arm pending TCP connect subtask
	return 0
}

func netpollclose(fd uintptr) int32 {
	if fd < maxFDHandles {
		wasifd_SetRuntimeCtx(int(fd), 0)
		for i := range fdHandles[fd] {
			h := fdHandles[fd][i]
			if h != 0 {
				// pollsetDeleteHandle clears pd/mode/gp/drop but
				// preserves result so that SubtaskCancelAndWait and
				// PollsetDisarm can detect terminal subtask state.
				pollsetDeleteHandle(h)
				fdHandles[fd][i] = 0
			}
		}
	}
	return 0
}

func netpollarm(pd *pollDesc, mode int) {
	throw("runtime: unused")
}

func netpollBreak() {}

// pendingTimerCancel tracks a timer subtask whose async cancel has not
// yet completed. While set (!= -1), netpoll skips creating new timers
// to avoid handle accumulation. The event loop auto-drops the handle
// when the cancel completion event arrives (drop=true in the async waiter).
var pendingTimerCancel int32 = -1

// netpoll blocks until an async event fires or the delay expires.
// On wasip3 this works by calling pausePC() to yield control to the host.
// When a callback fires, the trampoline stores event parameters and resumes
// execution here. netpoll then processes the event and returns woken goroutines.
func netpoll(delay int64) (gList, int32) {
	if delay == 0 {
		return gList{}, 0
	}

	pollsetInit()

	// Set up timer if the scheduler has a deadline, and no previous
	// timer cancel is still pending.
	timerHandle := int32(-1)
	if delay > 0 && pendingTimerCancel == -1 {
		result := async_wait_for(delay)
		status := result & statusMask
		subtask := result >> handleShift
		if status != statusReturn {
			wasi.WaitableJoin(subtask, waitableSet)
			addAsyncWaiter(subtask, 0, true) // timer, no goroutine to wake
			timerHandle = subtask
		}
		// If statusReturn, timer already expired; scheduler will
		// handle it via checkTimers on next loop iteration.
	}

	if handleCount == 0 {
		return gList{}, 0 // nothing to wait for
	}

	// Yield to host. Resumes when callback fires.
	asyncReturnValue = callbackWait | (waitableSet << handleShift)
	pausePC()

	// Back from pause — process the event that woke us.
	event := asyncEventCode
	p1 := asyncEventP1
	p2 := asyncEventP2

	var toRun gList
	var delta int32

	for {
		delta += netpollProcessEvent(&toRun, event, p1, p2, &timerHandle)

		// Check if the pending timer cancel completed during event processing.
		if pendingTimerCancel != -1 && handles[pendingTimerCancel].gp == 0 && !handles[pendingTimerCancel].drop {
			pendingTimerCancel = -1
		}

		// Poll for more pending events without yielding to the host.
		// The callback delivered one event, but others may be ready.
		var pollBuf [2]int32
		event = wasi.WaitableSetPoll(waitableSet, noescape(unsafe.Pointer(&pollBuf)))
		if event == 0 {
			break // no more pending events
		}
		p1 = pollBuf[0]
		p2 = pollBuf[1]
	}

	// Clean up pending timer if a non-timer event woke us.
	if timerHandle != -1 {
		removeAsyncWaiter(timerHandle)
		result := wasi.SubtaskCancelAsync(timerHandle)
		if result == blocked {
			// Cancel pending — leave the handle in the waitable set
			// for auto-drop when the completion event arrives. Suppress
			// new timer creation until then to prevent accumulation.
			addAsyncWaiter(timerHandle, 0, true)
			pendingTimerCancel = timerHandle
		} else {
			wasi.SubtaskDrop(timerHandle)
		}
	}

	return toRun, delta
}
