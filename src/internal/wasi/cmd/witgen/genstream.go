// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build witgen

package main

import (
	"fmt"
	"strings"
)

// generateStreamFutureBuiltins generates CM stream/future builtins for a function.
func (g *Generator) generateStreamFutureBuiltins(w *strings.Builder, fn Function, iface Interface) {
	rf := g.resolveFunc(fn)
	modulePath := g.modulePath(iface)

	// Collect all stream and future types from params and results.
	var streamFutureTypes []streamFutureInfo
	for _, p := range rf.Params {
		g.collectStreamFuture(p.Type, &streamFutureTypes)
	}
	if rf.Result != nil {
		g.collectStreamFuture(rf.Result, &streamFutureTypes)
	}

	if len(streamFutureTypes) == 0 {
		return
	}

	goFuncName := g.goFuncName(rf)
	g.needsUnsafe = true
	fmt.Fprintf(w, "// --- CM builtins for %s ---\n\n", rf.Name)

	for _, sf := range streamFutureTypes {
		idx := sf.typeParamIdx
		if sf.isStream {
			// stream-new-N
			fmt.Fprintf(w, "//go:wasmimport %s [stream-new-%d]%s\n", modulePath, idx, rf.Name)
			fmt.Fprintf(w, "func %sStreamNew%d() int64\n\n", goFuncName, idx)

			// stream-read-N
			fmt.Fprintf(w, "//go:wasmimport %s [async-lower][stream-read-%d]%s\n", modulePath, idx, rf.Name)
			fmt.Fprintf(w, "//go:noescape\n")
			fmt.Fprintf(w, "func %sStreamRead%d(readable int32, ptr unsafe.Pointer, len int32) int32\n\n", goFuncName, idx)

			// stream-write-N
			fmt.Fprintf(w, "//go:wasmimport %s [async-lower][stream-write-%d]%s\n", modulePath, idx, rf.Name)
			fmt.Fprintf(w, "//go:noescape\n")
			fmt.Fprintf(w, "func %sStreamWrite%d(writable int32, ptr unsafe.Pointer, len int32) int32\n\n", goFuncName, idx)

			// stream-cancel-read-N (async-lower)
			fmt.Fprintf(w, "//go:wasmimport %s [async-lower][stream-cancel-read-%d]%s\n", modulePath, idx, rf.Name)
			fmt.Fprintf(w, "func %sStreamCancelRead%d(readable int32) int32\n\n", goFuncName, idx)

			// stream-cancel-write-N (async-lower)
			fmt.Fprintf(w, "//go:wasmimport %s [async-lower][stream-cancel-write-%d]%s\n", modulePath, idx, rf.Name)
			fmt.Fprintf(w, "func %sStreamCancelWrite%d(writable int32) int32\n\n", goFuncName, idx)

			// stream-drop-readable-N
			fmt.Fprintf(w, "//go:wasmimport %s [stream-drop-readable-%d]%s\n", modulePath, idx, rf.Name)
			fmt.Fprintf(w, "func %sStreamDropReadable%d(readable int32)\n\n", goFuncName, idx)

			// stream-drop-writable-N
			fmt.Fprintf(w, "//go:wasmimport %s [stream-drop-writable-%d]%s\n", modulePath, idx, rf.Name)
			fmt.Fprintf(w, "func %sStreamDropWritable%d(writable int32)\n\n", goFuncName, idx)
		} else {
			// future-new-N
			fmt.Fprintf(w, "//go:wasmimport %s [future-new-%d]%s\n", modulePath, idx, rf.Name)
			fmt.Fprintf(w, "func %sFutureNew%d() int64\n\n", goFuncName, idx)

			// future-read-N
			fmt.Fprintf(w, "//go:wasmimport %s [async-lower][future-read-%d]%s\n", modulePath, idx, rf.Name)
			fmt.Fprintf(w, "//go:noescape\n")
			fmt.Fprintf(w, "func %sFutureRead%d(readable int32, ptr unsafe.Pointer) int32\n\n", goFuncName, idx)

			// future-drop-readable-N
			fmt.Fprintf(w, "//go:wasmimport %s [future-drop-readable-%d]%s\n", modulePath, idx, rf.Name)
			fmt.Fprintf(w, "func %sFutureDropReadable%d(readable int32)\n\n", goFuncName, idx)

			// future-drop-writable-N
			fmt.Fprintf(w, "//go:wasmimport %s [future-drop-writable-%d]%s\n", modulePath, idx, rf.Name)
			fmt.Fprintf(w, "func %sFutureDropWritable%d(writable int32)\n\n", goFuncName, idx)
		}
	}
}

type streamFutureInfo struct {
	typeParamIdx int
	isStream     bool // true=stream, false=future
}

// collectStreamFuture walks a type tree and collects stream/future types.
func (g *Generator) collectStreamFuture(rt *ResolvedType, out *[]streamFutureInfo) {
	if rt == nil {
		return
	}
	switch rt.Kind {
	case KindStream:
		*out = append(*out, streamFutureInfo{typeParamIdx: len(*out), isStream: true})
	case KindFuture:
		*out = append(*out, streamFutureInfo{typeParamIdx: len(*out), isStream: false})
	case KindTuple:
		for _, f := range rt.Fields {
			g.collectStreamFuture(f.Type, out)
		}
	case KindResult:
		g.collectStreamFuture(rt.Ok, out)
		g.collectStreamFuture(rt.Err, out)
	case KindOption:
		g.collectStreamFuture(rt.Inner, out)
	case KindTypeAlias:
		g.collectStreamFuture(rt.Alias, out)
	}
}

// tupleHasStreamFuture reports whether a tuple type has any stream or future fields.
func (g *Generator) tupleHasStreamFuture(rt *ResolvedType) bool {
	for _, f := range rt.Fields {
		if f.Type != nil {
			ft := g.deref(f.Type)
			if ft.Kind == KindStream || ft.Kind == KindFuture {
				return true
			}
		}
	}
	return false
}

// generateStreamOps generates ops struct types for functions with stream/future
// builtins. Ops structs are zero-size types that implement StreamOps/FutureOps
// interfaces, providing direct dispatch to wasmimport functions without vtable
// pointer indirection.
func (g *Generator) generateStreamOps(w *strings.Builder, fn Function, iface Interface) {
	rf := g.resolveFunc(fn)

	// Collect all stream and future types.
	var sfTypes []streamFutureInfo
	for _, p := range rf.Params {
		g.collectStreamFuture(p.Type, &sfTypes)
	}
	if rf.Result != nil {
		g.collectStreamFuture(rf.Result, &sfTypes)
	}
	if len(sfTypes) == 0 {
		return
	}

	goFuncName := g.goFuncName(rf)
	g.needsWasiImport = true
	g.needsUnsafe = true

	fmt.Fprintf(w, "// --- Ops types for %s ---\n\n", rf.Name)

	// Emit ops struct types for each stream/future.
	for _, sf := range sfTypes {
		idx := sf.typeParamIdx
		if sf.isStream {
			opsName := goFuncName + fmt.Sprintf("StreamOps%d", idx)
			fmt.Fprintf(w, "type %s struct{}\n\n", opsName)
			fmt.Fprintf(w, "func (%s) Read(r int32, p unsafe.Pointer, n int32) int32 { return %sStreamRead%d(r, p, n) }\n", opsName, goFuncName, idx)
			fmt.Fprintf(w, "func (%s) Write(w int32, p unsafe.Pointer, n int32) int32 { return %sStreamWrite%d(w, p, n) }\n", opsName, goFuncName, idx)
			fmt.Fprintf(w, "func (%s) NewPair() int64 { return %sStreamNew%d() }\n", opsName, goFuncName, idx)
			fmt.Fprintf(w, "func (%s) DropReadable(r int32) { %sStreamDropReadable%d(r) }\n", opsName, goFuncName, idx)
			fmt.Fprintf(w, "func (%s) DropWritable(w int32) { %sStreamDropWritable%d(w) }\n\n", opsName, goFuncName, idx)
		} else {
			opsName := goFuncName + fmt.Sprintf("FutureOps%d", idx)
			fmt.Fprintf(w, "type %s struct{}\n\n", opsName)
			fmt.Fprintf(w, "func (%s) Read(r int32, p unsafe.Pointer) int32 { return %sFutureRead%d(r, p) }\n", opsName, goFuncName, idx)
			fmt.Fprintf(w, "func (%s) DropReadable(r int32) { %sFutureDropReadable%d(r) }\n\n", opsName, goFuncName, idx)
		}
	}
}

// streamOpsName returns the ops type name for a stream at the given index.
func streamOpsName(goFuncName string, idx int) string {
	return goFuncName + fmt.Sprintf("StreamOps%d", idx)
}

// futureOpsName returns the ops type name for a future at the given index.
func futureOpsName(goFuncName string, idx int) string {
	return goFuncName + fmt.Sprintf("FutureOps%d", idx)
}
