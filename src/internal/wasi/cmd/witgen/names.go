// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build witgen

package main

import (
	"fmt"
	"strings"
)

// typeDescStr returns a human-readable description of a type for comments.
func (g *Generator) typeDescStr(rt *ResolvedType) string {
	if rt != nil {
		return strings.ToLower(g.shortTypeName(rt))
	}
	return "void"
}

func (g *Generator) resultStructName(rt *ResolvedType) string {
	okName := "Void"
	if rt.Ok != nil {
		okName = g.shortTypeName(rt.Ok)
	}
	errName := "Void"
	if rt.Err != nil {
		errName = g.shortTypeName(rt.Err)
	}
	return fmt.Sprintf("Result%s%s", okName, errName)
}

func (g *Generator) goType(rt *ResolvedType) string {
	if rt == nil {
		return "struct{}"
	}
	// Check if this type was renamed to avoid a collision.
	if renamed, ok := g.typeRenames[rt]; ok {
		return renamed
	}
	if rt.Kind == KindPrimitive {
		return primToGoType(rt.Primitive)
	}
	if rt.Name != "" {
		// Check if this type is from an imported package.
		if qualified, ok := g.importableType(rt); ok {
			return qualified
		}
		// Check if this is a local alias to an importable type.
		if rt.Kind == KindTypeAlias && rt.Alias != nil {
			if qualified, ok := g.importableType(rt.Alias); ok {
				return qualified
			}
		}
		return witToGoName(rt.Name)
	}
	// Anonymous type — describe inline.
	switch rt.Kind {
	case KindHandle:
		if rt.HandleTarget != nil && rt.HandleTarget.Name != "" {
			return witToGoName(rt.HandleTarget.Name)
		}
		return "int32"
	case KindStream:
		return "int32" // stream handle
	case KindFuture:
		return "int32" // future handle
	case KindList:
		if g.isPrim(rt.Inner, "u8") {
			return "wasi.ByteBuffer"
		}
		return "struct{ Ptr int32; Len int32 }"
	case KindResult:
		okName := "void"
		if rt.Ok != nil {
			okName = g.goType(rt.Ok)
		}
		errName := "void"
		if rt.Err != nil {
			errName = g.goType(rt.Err)
		}
		return fmt.Sprintf("Result_%s_%s", okName, errName)
	}
	return "int32"
}

// goSafeType returns the Go type for a type in the safe data model.
// Unlike goType which is for ABI/parameter purposes, goSafeType
// returns types suitable for Go struct fields and return values:
//   - option<T> → *GoType (pointer)
//   - list<string> → []string
//   - list<u8> → []byte
//   - list<T> → []GoType
//   - string → string
func (g *Generator) goSafeType(rt *ResolvedType) string {
	if rt == nil {
		return "struct{}"
	}
	orig := rt
	rt = g.deref(rt)
	if rt.Kind == KindPrimitive {
		// If the original was a named alias (e.g., "duration" → u64),
		// use the alias name, not the primitive type.
		if orig != rt {
			return g.goType(orig)
		}
		return primToGoType(rt.Primitive)
	}
	switch rt.Kind {
	case KindOption:
		inner := g.goSafeType(rt.Inner)
		return "*" + inner
	case KindList:
		if g.isPrim(rt.Inner, "u8") {
			return "wasi.ByteBuffer"
		}
		inner := g.goSafeType(rt.Inner)
		return "[]" + inner
	default:
		return g.goType(orig)
	}
}

// isPrim reports whether rt is a primitive type with the given name.
func (g *Generator) isPrim(rt *ResolvedType, prim string) bool {
	if rt == nil {
		return false
	}
	rt = g.deref(rt)
	return rt.Kind == KindPrimitive && rt.Primitive == prim
}

// modulePath returns the wasmimport module path for a WIT interface.
// The component model canonical format is "namespace:package/interface@version"
// (e.g. "wasi:sockets/types@0.3.0-rc-2026-02-09"), so we extract the version
// from the package name and place it at the end.
func (g *Generator) modulePath(iface Interface) string {
	pkg := g.doc.Packages[iface.Package]
	pkgName := pkg.Name
	version := ""
	if idx := strings.Index(pkgName, "@"); idx >= 0 {
		version = pkgName[idx:]
		pkgName = pkgName[:idx]
	}
	return fmt.Sprintf("%s/%s%s", pkgName, iface.Name, version)
}

func (g *Generator) goFuncName(rf ResolvedFunc) string {
	name := rf.Name
	// Remove [method]/[static]/[constructor] prefix.
	if strings.HasPrefix(name, "[method]") {
		name = strings.TrimPrefix(name, "[method]")
	} else if strings.HasPrefix(name, "[static]") {
		name = strings.TrimPrefix(name, "[static]")
		// Static named "resource.create" → NewResource (Go constructor convention).
		parts := strings.SplitN(name, ".", 2)
		if len(parts) == 2 && parts[1] == "create" {
			return "New" + witToGoName(parts[0])
		}
	} else if strings.HasPrefix(name, "[constructor]") {
		name = strings.TrimPrefix(name, "[constructor]")
		// Constructor: resource → NewResource
		parts := strings.SplitN(name, ".", 2)
		return "New" + witToGoName(parts[0])
	}
	// Convert resource.method to ResourceMethod.
	parts := strings.SplitN(name, ".", 2)
	goName := ""
	for _, p := range parts {
		goName += witToGoName(p)
	}
	if g.prefix != "" {
		goName = g.prefix + goName
	}
	return goName
}

// goMethodName extracts just the method part from a WIT method name.
// e.g., "[method]tcp-socket.listen" → "Listen"
func (g *Generator) goMethodName(rf ResolvedFunc) string {
	name := rf.Name
	for _, pfx := range []string{"[method]", "[async-method]"} {
		name = strings.TrimPrefix(name, pfx)
	}
	parts := strings.SplitN(name, ".", 2)
	if len(parts) == 2 {
		return witToGoName(parts[1])
	}
	return witToGoName(name)
}

// resourceGoName returns the Go type name for a resource type,
// respecting renames.
func (g *Generator) resourceGoName(rt *ResolvedType) string {
	if renamed, ok := g.typeRenames[rt]; ok {
		return renamed
	}
	return witToGoName(rt.Name)
}

// receiverName returns a short receiver variable name (first letter lowercase)
// for a resource type. e.g., "Descriptor" → "d", "TCPSocket" → "s".
func (g *Generator) receiverName(rt *ResolvedType) string {
	name := g.resourceGoName(rt)
	if name == "" {
		return "r"
	}
	return strings.ToLower(name[:1])
}

// unexport makes the first letter of a Go name lowercase.
func unexport(name string) string {
	if name == "" {
		return name
	}
	return strings.ToLower(name[:1]) + name[1:]
}

func (g *Generator) shortTypeName(rt *ResolvedType) string {
	if rt == nil {
		return "Void"
	}
	// Check name before deref to preserve named alias names (e.g., "duration" → "Duration").
	if rt.Name != "" {
		return witToGoName(rt.Name)
	}
	rt = g.deref(rt)
	if rt.Kind == KindPrimitive {
		return witToGoName(rt.Primitive)
	}
	switch rt.Kind {
	case KindHandle:
		if rt.HandleTarget != nil && rt.HandleTarget.Name != "" {
			return witToGoName(rt.HandleTarget.Name)
		}
	case KindStream:
		if rt.Inner != nil && rt.Inner.Idx >= 0 {
			return "Stream" + g.shortTypeName(rt.Inner)
		}
		return "Stream"
	case KindFuture:
		if rt.Inner != nil && rt.Inner.Idx >= 0 {
			return "Future" + g.shortTypeName(rt.Inner)
		}
		return "Future"
	case KindList:
		if rt.Inner != nil && rt.Inner.Idx >= 0 {
			return "List" + g.shortTypeName(rt.Inner)
		}
		return "List"
	case KindTuple:
		name := "Tuple"
		for _, f := range rt.Fields {
			if f.Type != nil {
				name += g.shortTypeName(f.Type)
			}
		}
		return name
	case KindResult:
		okName := "Void"
		if rt.Ok != nil {
			okName = g.shortTypeName(rt.Ok)
		}
		errName := "Void"
		if rt.Err != nil {
			errName = g.shortTypeName(rt.Err)
		}
		return "Result" + okName + errName
	}
	return fmt.Sprintf("T%d", rt.Idx)
}

// --- Name conversion utilities ---

// witToGoName converts a WIT kebab-case name to Go PascalCase.
func witToGoName(name string) string {
	parts := strings.Split(name, "-")
	var result string
	for _, p := range parts {
		result += capitalizeWord(p)
	}
	return result
}

// witToGoParamName converts a WIT kebab-case param name to Go camelCase.
func witToGoParamName(name string) string {
	parts := strings.Split(name, "-")
	var result string
	for i, p := range parts {
		if i == 0 {
			result += strings.ToLower(p)
		} else {
			result += capitalizeWord(p)
		}
	}
	// Avoid Go keywords.
	switch result {
	case "type", "func", "map", "range", "string", "error", "self":
		result += "_"
	}
	return result
}

// capitalizeWord handles common abbreviations.
func capitalizeWord(word string) string {
	upper := strings.ToUpper(word)
	switch upper {
	case "TCP", "UDP", "IP", "IPV4", "IPV6", "HTTP", "HTTPS",
		"DNS", "TLS", "URL", "URI", "ID", "EOF", "IO", "ABI":
		return upper
	}
	if len(word) == 0 {
		return ""
	}
	return strings.ToUpper(word[:1]) + word[1:]
}

func primToGoType(prim string) string {
	switch prim {
	case "bool":
		return "bool"
	case "u8":
		return "uint8"
	case "u16":
		return "uint16"
	case "u32":
		return "uint32"
	case "u64":
		return "uint64"
	case "s8":
		return "int8"
	case "s16":
		return "int16"
	case "s32":
		return "int32"
	case "s64":
		return "int64"
	case "f32":
		return "float32"
	case "f64":
		return "float64"
	case "char":
		return "rune"
	case "string":
		return "string"
	}
	return prim
}

// innerGoType returns the Go element type for a stream or list.
// e.g., stream<u8> → "byte", list<directory-entry> → "DirectoryEntry".
func (g *Generator) innerGoType(rt *ResolvedType) string {
	if rt.Inner != nil {
		inner := g.deref(rt.Inner)
		if inner.Kind == KindPrimitive {
			if inner.Primitive == "u8" {
				return "byte" // use byte instead of uint8 for stream<u8>
			}
			return primToGoType(inner.Primitive)
		}
		return g.goType(rt.Inner)
	}
	return "byte"
}
