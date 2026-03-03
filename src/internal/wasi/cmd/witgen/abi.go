// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build witgen

package main

import (
	"fmt"
	"strings"
)

// --- Canonical ABI glossary ---
//
// Canonical ABI: the component model's binary interface for translating
//   between high-level WIT types and low-level wasm values/memory.
//
// Flat params / flat results: a WIT function's parameters and results
//   represented as a sequence of core wasm value types (i32, i64, f32, f64).
//   Primitive types map 1:1; composite types (records, variants, etc.) are
//   recursively flattened into multiple flat values.
//
// Flattening: the process of expanding a WIT type into its flat
//   representation.  For example, a record{u32, u64} flattens to [i32, i64].
//
// params_ptr: when the flat param count exceeds maxFlatParams (sync) or
//   maxFlatAsyncParams (async), all params are packed into a linear-memory
//   buffer and a single i32 pointer is passed instead.
//
// retptr: when the flat result count exceeds maxFlatResults, the caller
//   passes an extra i32 pointer to a buffer where the callee writes the
//   result. Async functions always use retptr.
//
// Discriminant: the tag byte(s) at the start of a variant/option/result
//   that identifies the active case. Size depends on the number of cases
//   (≤256 → 1 byte, ≤65536 → 2 bytes, else 4 bytes).

// --- Primitive type info ---

type primType struct {
	goType string
	size   int
}

var primInfo = map[string]primType{
	"bool": {"byte", 1},
	"u8":   {"byte", 1}, "s8": {"int8", 1},
	"u16": {"uint16", 2}, "s16": {"int16", 2},
	"u32": {"uint32", 4}, "s32": {"int32", 4}, "char": {"uint32", 4},
	"u64": {"uint64", 8}, "s64": {"int64", 8},
	"f32": {"float32", 4}, "f64": {"float64", 8},
}

// variantPayloadOffset returns the byte offset of a variant's payload
// (after the discriminant and any alignment padding).
func (g *Generator) variantPayloadOffset(cases []Case) int {
	discSz, _ := g.discSize(len(cases))
	maxPayloadAlign := 1
	for _, c := range cases {
		if c.Type != nil {
			if _, ca := g.abiSize(c.Type); ca > maxPayloadAlign {
				maxPayloadAlign = ca
			}
		}
	}
	return alignTo(discSz, maxPayloadAlign)
}

// sizedType returns the unsigned integer type name for a given byte size.
func sizedType(size int) string {
	switch size {
	case 1:
		return "byte"
	case 2:
		return "uint16"
	case 4:
		return "uint32"
	case 8:
		return "uint64"
	}
	return "uint32"
}

// --- Buffer access ---

// BufAccess abstracts field-mode (r.b[off]) vs base-mode (unsafe.Add(base, off)) reads.
type BufAccess struct {
	bufExpr  string // e.g. "r.b", "buf.b"
	baseExpr string // e.g. "base"
}

func BufField(expr string) BufAccess { return BufAccess{bufExpr: expr} }
func BufBase() BufAccess             { return BufAccess{baseExpr: "base"} }

func (ba BufAccess) isField() bool { return ba.bufExpr != "" }

func (ba BufAccess) byteAt(off int) string {
	if ba.isField() {
		return fmt.Sprintf("%s[%d]", ba.bufExpr, off)
	}
	return fmt.Sprintf("*(*byte)(unsafe.Add(%s, %d))", ba.baseExpr, off)
}

func (ba BufAccess) ptrAt(off int) string {
	if ba.isField() {
		return fmt.Sprintf("unsafe.Pointer(&%s[%d])", ba.bufExpr, off)
	}
	return fmt.Sprintf("unsafe.Add(%s, %d)", ba.baseExpr, off)
}

// loadBuf returns a Go expression reading size bytes at off via ba, cast to goType.
func loadBuf(ba BufAccess, goType string, off, size int) string {
	if ba.isField() {
		if size == 1 {
			return fmt.Sprintf("%s(%s[%d])", goType, ba.bufExpr, off)
		}
		return fmt.Sprintf("%s(*(*%s)(unsafe.Pointer(&%s[%d])))", goType, sizedType(size), ba.bufExpr, off)
	}
	return fmt.Sprintf("%s(*(*%s)(unsafe.Add(%s, %d)))", goType, sizedType(size), ba.baseExpr, off)
}

func loadB(goType string, off, size int) string { return loadBuf(BufField("r.b"), goType, off, size) }

// emitReadExpr returns a Go expression reading a value from a buffer.
// Returns ("", false) for complex types that need multi-statement decode.
func (g *Generator) emitReadExpr(origRT *ResolvedType, ba BufAccess, off int) (string, bool) {
	rt := g.deref(origRT)
	goType := g.goType(origRT)

	switch rt.Kind {
	case KindPrimitive:
		if rt.Primitive == "string" {
			return "", false
		}
		if rt.Primitive == "bool" {
			return ba.byteAt(off) + " != 0", true
		}
		p, ok := primInfo[rt.Primitive]
		if !ok {
			return "", false
		}
		if p.size == 1 {
			return fmt.Sprintf("%s(%s)", goType, ba.byteAt(off)), true
		}
		return fmt.Sprintf("%s(*(*%s)(%s))", goType, p.goType, ba.ptrAt(off)), true
	case KindEnum, KindFlags:
		sz, _ := g.abiSize(rt)
		return loadBuf(ba, goType, off, sz), true
	case KindList:
		if !g.isPrim(rt.Inner, "u8") {
			return "", false // non-byte lists need multi-statement decode
		}
		g.needsWasiImport = true
		return fmt.Sprintf("wasi.ByteBuffer{Ptr: *(*int32)(%s), Len: *(*int32)(%s)}", ba.ptrAt(off), ba.ptrAt(off+4)), true
	case KindHandle, KindResource, KindStream, KindFuture:
		return loadBuf(ba, goType, off, 4), true
	case KindVariant:
		return fmt.Sprintf("*(*%s)(%s)", goType, ba.ptrAt(off)), true
	}

	return "", false
}

// emitRecordFieldsInline generates struct field initializers for a record/tuple.
func (g *Generator) emitRecordFieldsInline(w *strings.Builder, rt *ResolvedType, ba BufAccess, baseOff int, indent string) {
	offset := baseOff
	for i, f := range rt.Fields {
		var fieldName string
		if f.Name != "" {
			fieldName = witToGoName(f.Name)
		} else {
			fieldName = fmt.Sprintf("F%d", i)
		}
		fs, fa := g.abiSize(f.Type)
		offset = alignTo(offset, fa)

		if g.isPrim(f.Type, "string") {
			g.needsWasiImport = true
			fmt.Fprintf(w, "%s%s: wasi.CopyString(*(*int32)(%s), *(*int32)(%s)),\n",
				indent, fieldName, ba.ptrAt(offset), ba.ptrAt(offset+4))
		} else if expr, ok := g.emitReadExpr(f.Type, ba, offset); ok {
			fmt.Fprintf(w, "%s%s: %s,\n", indent, fieldName, expr)
		} else {
			// Fallback for complex types.
			fGoType := g.goType(f.Type)
			fmt.Fprintf(w, "%s%s: %s,\n", indent, fieldName, loadBuf(ba, fGoType, offset, fs))
		}
		offset += fs
	}
}

// storeStmt returns a Go statement storing expr at buf[off].
func storeStmt(buf, goType, expr string, off, size int) string {
	if size == 1 {
		return fmt.Sprintf("%s[%d] = %s(%s)", buf, off, goType, expr)
	}
	return fmt.Sprintf("*(*%s)(unsafe.Pointer(&%s[%d])) = %s(%s)", goType, buf, off, goType, expr)
}

// emitAlignedBuf emits a `var <name> struct { _ [0]int64; b [<size>]byte }` declaration.
func emitAlignedBuf(w *strings.Builder, name string, size any) {
	fmt.Fprintf(w, "\tvar %s struct {\n\t\t_ [0]int64\n\t\tb [%v]byte\n\t}\n", name, size)
}

// --- Canonical ABI size/alignment ---

// abiSize returns the byte size and alignment of a type in the canonical ABI.
func (g *Generator) abiSize(rt *ResolvedType) (size, align int) {
	if rt == nil {
		return 0, 1
	}
	rt = g.deref(rt)
	switch rt.Kind {
	case KindPrimitive:
		if rt.Primitive == "string" {
			return 8, 4 // ptr(i32) + len(i32)
		}
		if p, ok := primInfo[rt.Primitive]; ok {
			return p.size, p.size
		}
		return 4, 4 // fallback
	case KindTypeAlias:
		// Alias==nil fallback (deref handles non-nil).
		return 4, 4
	case KindEnum:
		if len(rt.Cases) <= 256 {
			return 1, 1
		}
		if len(rt.Cases) <= 65536 {
			return 2, 2
		}
		return 4, 4
	case KindFlags:
		n := len(rt.Flags)
		if n <= 8 {
			return 1, 1
		}
		if n <= 16 {
			return 2, 2
		}
		return ((n + 31) / 32) * 4, 4
	case KindRecord, KindTuple:
		return g.abiSizeOfRecord(rt.Fields)
	case KindVariant:
		return g.abiSizeOfVariant(rt.Cases)
	case KindResult:
		// Result is a variant with ok/err cases.
		cases := g.resultAsCases(rt)
		return g.abiSizeOfVariant(cases)
	case KindOption:
		// Option is a variant with none/some cases.
		cases := []Case{
			{Name: "none"},
			{Name: "some", Type: rt.Inner},
		}
		return g.abiSizeOfVariant(cases)
	case KindList:
		return 8, 4 // ptr + len
	case KindHandle, KindResource:
		return 4, 4
	case KindStream, KindFuture:
		return 4, 4 // handle
	}
	return 4, 4
}

func (g *Generator) abiSizeOfRecord(fields []Field) (size, align int) {
	align = 1
	offset := 0
	for _, f := range fields {
		fs, fa := g.abiSize(f.Type)
		if fa > align {
			align = fa
		}
		offset = alignTo(offset, fa)
		offset += fs
	}
	size = alignTo(offset, align)
	return
}

func (g *Generator) abiSizeOfVariant(cases []Case) (size, align int) {
	discSize, discAlign := g.discSize(len(cases))
	maxPayloadSize := 0
	maxPayloadAlign := 1
	for _, c := range cases {
		if c.Type == nil {
			continue
		}
		cs, ca := g.abiSize(c.Type)
		if cs > maxPayloadSize {
			maxPayloadSize = cs
		}
		if ca > maxPayloadAlign {
			maxPayloadAlign = ca
		}
	}
	align = max(discAlign, maxPayloadAlign)
	payloadOffset := alignTo(discSize, maxPayloadAlign)
	size = alignTo(payloadOffset+maxPayloadSize, align)
	return
}

func (g *Generator) discSize(numCases int) (size, align int) {
	if numCases <= 256 {
		return 1, 1
	}
	if numCases <= 65536 {
		return 2, 2
	}
	return 4, 4
}

// flatABIEntry maps a flat value to its byte offset and size in the ABI layout.
type flatABIEntry struct {
	offset int
	size   int // 1, 2, 4, or 8
}

// flatToABIMapping computes the ABI byte offset and store size for each flat
// value of a type. This is used to correctly pack flat param values into a
// params_ptr buffer where the ABI value layout has different sizes from the
// flat representation (e.g., u8 flags flatten to i32 but occupy 1 byte in ABI).
func (g *Generator) flatToABIMapping(rt *ResolvedType, baseOffset int) []flatABIEntry {
	if rt == nil {
		return nil
	}
	rt = g.deref(rt)
	switch rt.Kind {
	case KindPrimitive:
		if rt.Primitive == "string" {
			return []flatABIEntry{{baseOffset, 4}, {baseOffset + 4, 4}}
		}
		if p, ok := primInfo[rt.Primitive]; ok {
			return []flatABIEntry{{baseOffset, p.size}}
		}
		return []flatABIEntry{{baseOffset, 4}}
	case KindTypeAlias:
		if rt.Alias != nil {
			return g.flatToABIMapping(rt.Alias, baseOffset)
		}
		return []flatABIEntry{{baseOffset, 4}}
	case KindHandle, KindResource, KindStream, KindFuture:
		return []flatABIEntry{{baseOffset, 4}}
	case KindEnum, KindFlags:
		sz, _ := g.abiSize(rt)
		return []flatABIEntry{{baseOffset, sz}}
	case KindRecord, KindTuple:
		var result []flatABIEntry
		offset := baseOffset
		for _, f := range rt.Fields {
			fs, fa := g.abiSize(f.Type)
			offset = alignTo(offset, fa)
			result = append(result, g.flatToABIMapping(f.Type, offset)...)
			offset += fs
		}
		return result
	case KindList:
		return []flatABIEntry{{baseOffset, 4}, {baseOffset + 4, 4}}
	case KindOption:
		// Option is a 2-case variant: none (empty) and some(inner).
		cases := []Case{
			{Name: "none"},
			{Name: "some", Type: rt.Inner},
		}
		return g.flatToABIMappingVariant(cases, baseOffset)
	case KindVariant:
		return g.flatToABIMappingVariant(rt.Cases, baseOffset)
	}
	return []flatABIEntry{{baseOffset, 4}}
}

// flatToABIMappingVariant returns ABI byte offset mappings for a variant type,
// including the discriminant and the max case's payload fields.
func (g *Generator) flatToABIMappingVariant(cases []Case, baseOffset int) []flatABIEntry {
	discSz, _ := g.discSize(len(cases))
	payloadOff := g.variantPayloadOffset(cases)

	// Disc mapping entry.
	result := []flatABIEntry{{baseOffset, discSz}}

	// Find the max case (most flat values).
	maxCaseIdx := -1
	maxCaseFlat := 0
	for ci, c := range cases {
		if c.Type == nil {
			continue
		}
		cflat := len(g.flatten(c.Type))
		if cflat > maxCaseFlat {
			maxCaseFlat = cflat
			maxCaseIdx = ci
		}
	}

	if maxCaseIdx >= 0 {
		mc := cases[maxCaseIdx]
		result = append(result, g.flatToABIMapping(mc.Type, baseOffset+payloadOff)...)
	}
	return result
}

func (g *Generator) resultAsCases(rt *ResolvedType) []Case {
	return []Case{
		{Name: "ok", Type: rt.Ok},
		{Name: "err", Type: rt.Err},
	}
}

// --- Flattening for function signatures ---

func (g *Generator) flatten(rt *ResolvedType) []WasmValType {
	if rt == nil {
		return nil
	}
	rt = g.deref(rt)
	switch rt.Kind {
	case KindPrimitive:
		switch rt.Primitive {
		case "bool", "u8", "s8", "u16", "s16", "u32", "s32", "char":
			return []WasmValType{WasmI32}
		case "u64", "s64":
			return []WasmValType{WasmI64}
		case "f32":
			return []WasmValType{WasmF32}
		case "f64":
			return []WasmValType{WasmF64}
		case "string":
			return []WasmValType{WasmI32, WasmI32}
		}
		return []WasmValType{WasmI32}
	case KindTypeAlias:
		if rt.Alias != nil {
			return g.flatten(rt.Alias)
		}
		return []WasmValType{WasmI32}
	case KindEnum:
		return []WasmValType{WasmI32}
	case KindFlags:
		n := (len(rt.Flags) + 31) / 32
		if n == 0 {
			n = 1
		}
		out := make([]WasmValType, n)
		for i := range out {
			out[i] = WasmI32
		}
		return out
	case KindRecord, KindTuple:
		var out []WasmValType
		for _, f := range rt.Fields {
			out = append(out, g.flatten(f.Type)...)
		}
		return out
	case KindVariant:
		return g.flattenVariant(rt.Cases)
	case KindResult:
		cases := g.resultAsCases(rt)
		return g.flattenVariant(cases)
	case KindOption:
		cases := []Case{
			{Name: "none"},
			{Name: "some", Type: rt.Inner},
		}
		return g.flattenVariant(cases)
	case KindList:
		return []WasmValType{WasmI32, WasmI32}
	case KindHandle, KindResource:
		return []WasmValType{WasmI32}
	case KindStream, KindFuture:
		return []WasmValType{WasmI32}
	}
	return []WasmValType{WasmI32}
}

func (g *Generator) flattenVariant(cases []Case) []WasmValType {
	// disc + max(flat(case))
	maxFlat := 0
	var widest []WasmValType
	for _, c := range cases {
		if c.Type == nil {
			continue
		}
		flat := g.flatten(c.Type)
		if len(flat) > maxFlat {
			maxFlat = len(flat)
			widest = flat
		}
	}
	out := []WasmValType{WasmI32} // discriminant
	// Pad all cases to the same length, using the widest case's types.
	for i := 0; i < maxFlat; i++ {
		if i < len(widest) {
			out = append(out, widest[i])
		} else {
			out = append(out, WasmI32)
		}
	}
	return out
}

// deref follows type aliases to the underlying type.
func (g *Generator) deref(rt *ResolvedType) *ResolvedType {
	for rt != nil && rt.Kind == KindTypeAlias && rt.Alias != nil {
		rt = rt.Alias
	}
	if rt == nil {
		panic("deref: nil type after alias resolution")
	}
	return rt
}

func alignTo(offset, align int) int {
	return (offset + align - 1) &^ (align - 1)
}
