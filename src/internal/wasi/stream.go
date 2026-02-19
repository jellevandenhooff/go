// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package wasi

import (
	"unsafe"
)

// --- Stream dispatch via generic ops ---

// StreamOps provides CM stream operations for a specific stream type.
// Implemented as zero-size structs in generated code for direct dispatch.
type StreamOps interface {
	Read(readable int32, ptr unsafe.Pointer, len int32) int32
	Write(writable int32, ptr unsafe.Pointer, len int32) int32
	NewPair() int64
	DropReadable(readable int32)
	DropWritable(writable int32)
}

// StreamReader reads from a component model stream.
// Ops is a zero-size struct that dispatches to the correct wasmimport functions.
type StreamReader[Ops StreamOps, T any] struct {
	Handle int32
}

// Read reads elements from the stream. Returns raw copy status.
// Caller checks for Blocked and calls UnpackCopy.
func (r StreamReader[Ops, T]) Read(buf []T) int32 {
	if len(buf) == 0 {
		return 0
	}
	var ops Ops
	result := ops.Read(r.Handle, unsafe.Pointer(&buf[0]), int32(len(buf)))
	KeepAlive(buf)
	return result
}

// Drop releases the readable end of the stream.
func (r StreamReader[Ops, T]) Drop() {
	if r.Handle != 0 {
		var ops Ops
		ops.DropReadable(r.Handle)
	}
}

// StreamWriter writes to a component model stream.
// Ops is a zero-size struct that dispatches to the correct wasmimport functions.
type StreamWriter[Ops StreamOps, T any] struct {
	Handle int32
}

// Write writes elements to the stream. Returns raw copy status.
// Caller checks for Blocked and calls UnpackCopy.
func (w StreamWriter[Ops, T]) Write(data []T) int32 {
	if len(data) == 0 {
		return 0
	}
	var ops Ops
	result := ops.Write(w.Handle, unsafe.Pointer(&data[0]), int32(len(data)))
	KeepAlive(data)
	return result
}

// Drop releases the writable end of the stream.
func (w StreamWriter[Ops, T]) Drop() {
	if w.Handle != 0 {
		var ops Ops
		ops.DropWritable(w.Handle)
	}
}

// NewStreamPair creates a new stream reader/writer pair.
func NewStreamPair[Ops StreamOps, T any]() (StreamReader[Ops, T], StreamWriter[Ops, T]) {
	var ops Ops
	pair := ops.NewPair()
	return StreamReader[Ops, T]{Handle: int32(pair)},
		StreamWriter[Ops, T]{Handle: int32(pair >> 32)}
}

// --- Future dispatch via generic ops ---

// FutureOps provides CM future operations for a specific future type.
type FutureOps interface {
	Read(readable int32, ptr unsafe.Pointer) int32
	DropReadable(readable int32)
}

// FutureReader reads a typed result from a component model future.
// Ops is a zero-size struct that dispatches to the correct wasmimport functions.
type FutureReader[Ops FutureOps, T any] struct {
	Handle int32
}

// Wait blocks until the future completes and returns the typed result.
// The result buffer is heap-allocated so the pointer remains valid
// if the goroutine stack grows while parked in WaitForWaitable.
func (f FutureReader[Ops, T]) Wait() T {
	var ops Ops
	retptr := AllocResult(int32(unsafe.Sizeof(*new(T))))
	status := ops.Read(f.Handle, retptr)
	if status == Blocked {
		WaitForWaitable(f.Handle)
	}
	result := *(*T)(retptr)
	FreeResult(retptr)
	return result
}

// Drop releases the readable end of the future.
func (f FutureReader[Ops, T]) Drop() {
	if f.Handle != 0 {
		var ops Ops
		ops.DropReadable(f.Handle)
	}
}

// WriteStreamBytes writes data to a stream and waits for the associated future.
// It writes data to the writer, drops the writer, waits for the future result,
// and drops the future. Returns the number of bytes written and the future result.
func WriteStreamBytes[SOps StreamOps, FOps FutureOps, R any](
	writer StreamWriter[SOps, byte], future FutureReader[FOps, R], data []byte,
) (int, R) {
	writeResult := writer.Write(data)
	if writeResult == Blocked {
		writeResult = WaitForWaitable(writer.Handle)
	}
	_, n := UnpackCopy(writeResult)
	writer.Drop()
	result := future.Wait()
	future.Drop()
	return n, result
}

// ReadStreamBytes reads data from a stream and waits for the associated future.
// It reads into buf from the reader, drops the reader, waits for the future result,
// and drops the future. Returns the number of bytes read and the future result.
func ReadStreamBytes[SOps StreamOps, FOps FutureOps, R any](
	reader StreamReader[SOps, byte], future FutureReader[FOps, R], buf []byte,
) (int, R) {
	readResult := reader.Read(buf)
	if readResult == Blocked {
		readResult = WaitForWaitable(reader.Handle)
	}
	_, n := UnpackCopy(readResult)
	reader.Drop()
	result := future.Wait()
	future.Drop()
	return n, result
}
