// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "encoding/json"

// --- JSON schema for wasm-tools component wit --json ---

type WITDoc struct {
	Packages   []Package   `json:"packages"`
	Interfaces []Interface `json:"interfaces"`
	Types      []TypeDef   `json:"types"`
}

type Package struct {
	Name       string         `json:"name"`
	Interfaces map[string]int `json:"interfaces"`
}

type Interface struct {
	Name      string              `json:"name"`
	Types     map[string]int      `json:"types"`
	Functions map[string]Function `json:"functions"`
	Package   int                 `json:"package"`
}

type Function struct {
	Name   string          `json:"name"`
	Kind   json.RawMessage `json:"kind"`
	Params []Param         `json:"params"`
	Result json.RawMessage `json:"result"`
}

type Param struct {
	Name string          `json:"name"`
	Type json.RawMessage `json:"type"`
}

type TypeDef struct {
	Name  *string         `json:"name"`
	Kind  json.RawMessage `json:"kind"`
	Owner json.RawMessage `json:"owner"`
}

// --- Resolved type kinds ---

type TypeKind int

const (
	KindPrimitive TypeKind = iota
	KindRecord
	KindTuple
	KindVariant
	KindEnum
	KindFlags
	KindOption
	KindResult
	KindList
	KindHandle
	KindResource
	KindStream
	KindFuture
	KindTypeAlias
)

type ResolvedType struct {
	Kind TypeKind
	Name string // empty for anonymous types
	Idx  int    // original WIT doc type index, -1 for interned primitives

	// OwnerInterface is the index of the interface that owns this type,
	// parsed once from the JSON "owner" field. -1 means no owner.
	OwnerInterface int

	// Primitive: "u8", "u16", "u32", "u64", "s8", "s16", "s32", "s64",
	//            "f32", "f64", "bool", "char", "string"
	Primitive string

	// Record/Tuple
	Fields []Field

	// Variant/Enum
	Cases []Case

	// Flags
	Flags []string

	// Option<T>, List<T>, Stream<T>, Future<T>
	Inner *ResolvedType

	// Result<Ok, Err>
	Ok  *ResolvedType
	Err *ResolvedType

	// Handle: own or borrow
	HandleOwn    bool
	HandleTarget *ResolvedType

	// TypeAlias → another type
	Alias *ResolvedType
}

type Field struct {
	Name string
	Type *ResolvedType // nil for void
}

type Case struct {
	Name string
	Type *ResolvedType // nil for no payload
}

// --- Function kind ---

type FuncKind int

const (
	FuncFreestanding FuncKind = iota
	FuncMethod
	FuncStatic
	FuncConstructor
	FuncAsyncFreestanding
	FuncAsyncMethod
	FuncAsyncStatic
)

type ResolvedFunc struct {
	Name     string
	Kind     FuncKind
	Resource *ResolvedType // resource for method/static/constructor
	Params   []ResolvedParam
	Result   *ResolvedType // nil for no result
	IsAsync  bool
}

type ResolvedParam struct {
	Name string
	Type *ResolvedType
}

// --- Canonical ABI ---

type WasmValType int

const (
	WasmI32 WasmValType = iota
	WasmI64
	WasmF32
	WasmF64
)

func (w WasmValType) GoType() string {
	switch w {
	case WasmI32:
		return "int32"
	case WasmI64:
		return "int64"
	case WasmF32:
		return "float32"
	case WasmF64:
		return "float64"
	}
	return "int32"
}

const (
	// maxFlatParams is the maximum number of flat core-wasm values a sync
	// function may pass as individual parameters before switching to params_ptr.
	maxFlatParams = 16
	// maxFlatAsyncParams is the async equivalent of maxFlatParams; the lower
	// limit reflects the more constrained async ABI calling convention.
	maxFlatAsyncParams = 4
	// maxFlatResults is the maximum number of flat core-wasm values a function
	// may return directly before the caller must supply a retptr buffer.
	maxFlatResults = 1
)
