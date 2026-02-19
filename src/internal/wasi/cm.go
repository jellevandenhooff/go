// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

// Package wasi provides constants and helpers for the WebAssembly Component
// Model async ABI used by WASI preview 3 (wasip3).
//
// This package isolates CM-specific conventions from the rest of the Go
// runtime, making changes to the standard library easier to review.
//
// # GC Safety
//
// On wasm32, linear memory addresses and Go pointers share the same 32-bit
// address space. The GC uses static type information to decide which fields
// to trace: *byte is traced, int32 and [N]byte are not.
//
// Result/variant/option return values from wasmimport calls are represented
// as tagged unions. The payload area uses [N]byte — the GC never traces
// union payloads for pointers. This avoids the GC "shape" problem where
// a pointer-typed union field (e.g. *byte) would cause the GC to trace
// garbage bits when the union holds a non-pointer case (e.g. an error code).
//
// String and list data returned by the host lives in cabi_realloc buffers
// (Go []byte slices). Each allocation is prefixed with an 8-byte header
// that stores the buffer's total size (header + data). The host receives a
// pointer past the header.
//
// # Buffer Lifecycle
//
// When the host returns a string or list, it calls cabi_realloc to allocate
// space in linear memory. cabi_realloc either reuses a buffer from the free
// list or allocates a new Go []byte slice (kept alive permanently in
// allocBufs). The pointer returned to the host points past the 8-byte header.
//
//  1. cabi_realloc allocates [header | data...] and returns &data
//  2. Host writes string/list data into the data area
//  3. Go code copies data out (CopyString, generated wrappers, etc.)
//  4. FreeRealloc(ptr) backs up to the header, reads the size, and pushes
//     the buffer onto the free list for reuse
//
// No scanning or lookup is needed — allocation and freeing are both O(1).
package wasi

import "unsafe"

// ByteBuffer holds a (ptr, len) pair returned by the canonical ABI for
// list<u8> payloads inside result structs. Unlike raw int32 pairs, ByteBuffer
// carries the data as a single value and provides CopyInto for safe extraction.
type ByteBuffer struct{ Ptr, Len int32 }

// CopyInto copies the list<u8> data from linear memory into dst, frees the
// cabi_realloc buffer, and zeroes the ByteBuffer to prevent double-free.
// Returns the number of bytes copied.
func (b *ByteBuffer) CopyInto(dst []byte) int {
	if b.Len == 0 || b.Ptr == 0 {
		return 0
	}
	src := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(b.Ptr))), b.Len)
	n := copy(dst, src)
	FreeRealloc(b.Ptr)
	b.Ptr = 0
	b.Len = 0
	return n
}

// alwaysFalse is used by KeepAlive to prevent the compiler from eliminating
// the reference. This is the same pattern as runtime.KeepAlive (which uses
// cgoAlwaysFalse). Having a local copy here breaks the import cycle:
// internal/wasi sub-packages no longer need to import "runtime".
var alwaysFalse bool

// CabiUseStatic is set by runtime netpoll when running on g0 to signal
// cabiRealloc to use static buffers instead of allocating via make()
// (which panics on g0). This enables println for debugging.
// Safe without synchronization: wasm is single-threaded.
var CabiUseStatic bool

// KeepAlive marks its argument as reachable at the point of the call.
// This ensures that the object is not freed, and its finalizer is not run,
// before the point in the program where KeepAlive is called.
//
// This is a local copy of runtime.KeepAlive. The real runtime.KeepAlive is
// a compiler intrinsic for liveness analysis; this copy works by preventing
// the compiler from proving the reference is dead (via a never-true var).
// Having the copy here avoids importing "runtime", which would create an
// import cycle since runtime imports internal/wasi sub-packages.
func KeepAlive(x any) {
	if alwaysFalse {
		println(x)
	}
}

// Static buffers for cabi_realloc when cabiUseStatic is true.
// These avoid make() which panics on the system stack (g0).
const numStaticBufs = 4
const staticBufSize = 1024

var staticBufs [numStaticBufs][staticBufSize]byte
var staticBufIdx int

// StringToPtr converts a Go string to its canonical ABI representation
// as (ptr, len) int32 pair for passing to wasmimport functions.
//
// The returned int32 pointer is not traced by the GC. Callers must ensure
// the original string remains reachable (e.g. via runtime.KeepAlive) until
// the wasmimport call that consumes the pointer has returned.
func StringToPtr(s string) (int32, int32) {
	return int32(uintptr(unsafe.Pointer(unsafe.StringData(s)))), int32(len(s))
}

// CopyString copies a canonical ABI (ptr, len) string from linear memory
// into a Go-owned string and frees the cabi_realloc buffer that backs it.
// The returned string does not reference linear memory.
func CopyString(ptr int32, length int32) string {
	if length == 0 {
		return ""
	}
	src := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
	dst := make([]byte, length)
	copy(dst, src)
	FreeRealloc(ptr)
	return unsafe.String(unsafe.SliceData(dst), len(dst))
}

// --- cabi_realloc ---
//
// Each allocation is structured as [header (8 bytes) | data...].
// The header stores the total buffer size as an int32 at offset 0.
// The 8-byte header maintains alignment for the data area.
//
// Freed buffers are placed into size-class buckets (power-of-2 sizes
// starting at 16 bytes) for O(1) reuse. All allocations within a size
// class have the same capacity, so any buffer from the right bucket fits.

const reallocHeaderSize = 8

// Size classes: 16, 32, 64, 128, ..., 16<<(numSizeClasses-1).
// Class i holds buffers of exactly minClassSize<<i bytes.
const (
	minClassSize   = 16
	numSizeClasses = 16 // 16 bytes to 512KB
)

// sizeClassOf returns the size class for a given buffer size.
func sizeClassOf(size int) int {
	if size <= minClassSize {
		return 0
	}
	class := 0
	s := (size - 1) >> 4 // divide by minClassSize
	for s > 0 {
		s >>= 1
		class++
	}
	if class >= numSizeClasses {
		return numSizeClasses - 1
	}
	return class
}

//go:wasmexport cabi_realloc
func cabiRealloc(oldPtr, oldSize, align, newSize int32) int32 {
	// On g0 (system stack), make() panics because mallocgc is not
	// allowed. Use pre-allocated static buffers instead.
	if CabiUseStatic {
		return staticRealloc(oldPtr, oldSize, newSize)
	}

	totalSize := int(reallocHeaderSize) + int(newSize)

	// Round up to the size class boundary so all buffers in a class
	// are interchangeable.
	class := sizeClassOf(totalSize)
	if rounded := minClassSize << class; rounded > totalSize {
		totalSize = rounded
	}

	var buf []byte
	// Pop from the size-class bucket (O(1), guaranteed to fit).
	if bucket := freeBuckets[class]; len(bucket) > 0 {
		n := len(bucket)
		buf = bucket[n-1][:totalSize]
		freeBuckets[class][n-1] = nil
		freeBuckets[class] = bucket[:n-1]
	} else {
		buf = make([]byte, totalSize)
		// Permanently keep this allocation alive. The GC cannot trace
		// int32 pointers held by the host, so we must retain a Go
		// reference to prevent collection. Buffers are never removed.
		allocBufs = append(allocBufs, buf)
	}

	// Write total size into the header.
	*(*int32)(unsafe.Pointer(&buf[0])) = int32(totalSize)

	// Pointer to the data area (past the header). We use pointer arithmetic
	// instead of &buf[reallocHeaderSize] to avoid a bounds-check panic when
	// newSize is 0 (the host may allocate 0-byte buffers for empty strings).
	dataPtr := int32(uintptr(unsafe.Pointer(unsafe.SliceData(buf))) + reallocHeaderSize)

	if oldPtr != 0 && oldSize > 0 {
		old := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(oldPtr))), oldSize)
		copy(buf[reallocHeaderSize:], old)
		FreeRealloc(oldPtr)
	}

	return dataPtr
}

// staticRealloc uses pre-allocated static buffers for cabi_realloc
// when running on g0 (where make() would panic).
func staticRealloc(oldPtr, oldSize, newSize int32) int32 {
	totalSize := int(reallocHeaderSize) + int(newSize)
	if totalSize > staticBufSize {
		// Can't handle large allocations on g0. This shouldn't happen
		// for debug println (small writes to stderr).
		panic("cabi_realloc: static allocation too large")
	}

	idx := staticBufIdx % numStaticBufs
	staticBufIdx++

	buf := &staticBufs[idx]
	// Write total size into the header.
	*(*int32)(unsafe.Pointer(&buf[0])) = int32(totalSize)

	dataPtr := int32(uintptr(unsafe.Pointer(&buf[reallocHeaderSize])))

	if oldPtr != 0 && oldSize > 0 {
		src := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(oldPtr))), oldSize)
		dst := buf[reallocHeaderSize:]
		copy(dst, src)
	}

	return dataPtr
}

// allocBufs permanently holds references to all buffers ever allocated by
// cabi_realloc, preventing GC from collecting the backing arrays while
// the host references them as int32 pointers. Buffers are never removed.
var allocBufs [][]byte

// freeBuckets holds reusable buffers grouped by size class. Each bucket
// is a stack of buffers with the same capacity. FreeRealloc pushes here;
// cabi_realloc pops.
var freeBuckets [numSizeClasses][][]byte

// Pre-allocate buffers so cabiRealloc never needs to call make() at
// runtime. This avoids GC allocation pressure and future nosplit issues.
const (
	numPreallocClasses = 6 // classes 0..5: 16, 32, 64, 128, 256, 512 bytes
	bufsPerClass       = 4
)

func init() {
	for class := 0; class < numPreallocClasses; class++ {
		size := minClassSize << class
		for i := 0; i < bufsPerClass; i++ {
			buf := make([]byte, size)
			allocBufs = append(allocBufs, buf)
			freeBuckets[class] = append(freeBuckets[class], buf)
		}
	}
}

// AllocResult allocates a result buffer from the realloc pool for use
// as a retptr in async (non-noescape) wasmimport calls. The returned
// pointer is stable (not on the goroutine stack), so the host can
// safely write to it after stack growth. Call FreeResult when done.
func AllocResult(size int32) unsafe.Pointer {
	return unsafe.Pointer(uintptr(cabiRealloc(0, 0, 8, size)))
}

// FreeResult returns a result buffer (obtained from [AllocResult])
// to the realloc pool for reuse.
func FreeResult(ptr unsafe.Pointer) {
	FreeRealloc(int32(uintptr(ptr)))
}

// FreeRealloc returns a cabi_realloc buffer to the appropriate size-class
// bucket for reuse. It reads the buffer's total size from the 8-byte
// header that precedes the data pointer. The buffer remains alive via
// allocBufs.
//
// Called automatically by [CopyString] and by generated list wrappers
// after data has been copied into Go-owned memory.
func FreeRealloc(ptr int32) {
	if ptr == 0 {
		return
	}
	headerPtr := uintptr(ptr) - reallocHeaderSize
	totalSize := int(*(*int32)(unsafe.Pointer(headerPtr)))
	buf := unsafe.Slice((*byte)(unsafe.Pointer(headerPtr)), totalSize)
	class := sizeClassOf(totalSize)
	freeBuckets[class] = append(freeBuckets[class], buf)
}

// --- Async ABI constants ---

// When a function is imported with [async-lower], it returns a packed i32:
//
//	status = (subtask_handle << HandleShift) | status_code
//
// If status_code == StatusReturn, the function completed synchronously
// and the result is already written to the retptr.
const (
	StatusMask   int32 = 0xf
	HandleShift        = 4
	StatusReturn int32 = 2
)

// Blocked is returned by stream/future read/write builtins when the
// operation cannot make progress and the caller must wait.
const Blocked int32 = -1 // 0xffffffff

// Stream/future read and write builtins return a packed i32:
//
//	result = copy_result | (progress << 4)
const (
	CopyCompleted = 0
	CopyDropped   = 1 // EOF / stream closed
	CopyCancelled = 2
)

// UnpackCopy unpacks a stream/future read/write result.
func UnpackCopy(packed int32) (copyResult int, progress int) {
	return int(packed & 0xf), int(packed >> 4)
}

// IsReturned reports whether an async-lower status indicates synchronous completion.
func IsReturned(status int32) bool {
	return status&StatusMask == StatusReturn
}

// Subtask extracts the subtask handle from an async-lower status.
func Subtask(status int32) int32 {
	return status >> HandleShift
}
