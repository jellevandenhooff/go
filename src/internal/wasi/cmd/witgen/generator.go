// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build witgen

package main

import (
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GenSpec describes one interface-to-file mapping.
type GenSpec struct {
	IfaceName string // "wasi:clocks@0.3.0-rc-2026-02-09/types"
	IfaceIdx  int    // resolved index into doc.Interfaces
	Filename  string // "types.go"
	Prefix    string // "Monotonic"
}

// ImportSpec maps a WIT package to a Go import path, allowing cross-package
// type references instead of generating local copies.
type ImportSpec struct {
	WITPkg   string // "wasi:clocks@0.3.0-rc-2026-02-09"
	GoImport string // "internal/wasi/clocks"
	GoAlias  string // "clocks"
}

// --- Generator state ---

type Generator struct {
	doc      *WITDoc
	types    []ResolvedType
	pkgName  string // Go package name
	buildTag string // optional build constraint
	outDir   string // output directory
	specs    []GenSpec

	// Cross-package imports.
	importSpecs []ImportSpec

	// Cross-file state (not reset between files).
	generatedTypes   map[string]bool          // tracks type Go names already emitted
	generatedResults map[string]bool          // tracks result struct names already emitted
	typeRenames      map[*ResolvedType]string // type pointers to renamed Go names

	// Per-file state (reset between files).
	ifaceName       string // current interface name (from spec)
	ifaceIdx        int    // current interface index (from spec)
	prefix          string // current function name prefix (from spec)
	needsWasiImport bool
	needsUnsafe     bool
	fileImports     map[string]string // Go import path → alias, used imports for this file

	// Interned primitive types.
	primTypes map[string]*ResolvedType
}

// primType returns a *ResolvedType for the given primitive type name,
// creating one if it doesn't exist.
func (g *Generator) primType(prim string) *ResolvedType {
	if rt, ok := g.primTypes[prim]; ok {
		return rt
	}
	rt := &ResolvedType{Kind: KindPrimitive, Primitive: prim, Idx: -1, OwnerInterface: -1}
	g.primTypes[prim] = rt
	return rt
}

// resolveTypeRef interprets a JSON value as a type reference:
// - integer → pointer to resolved type in g.types
// - string  → interned primitive type
// - null    → nil
func (g *Generator) resolveTypeRef(raw json.RawMessage) *ResolvedType {
	if raw == nil || string(raw) == "null" {
		return nil
	}
	var idx int
	if err := json.Unmarshal(raw, &idx); err == nil {
		return &g.types[idx]
	}
	var prim string
	if err := json.Unmarshal(raw, &prim); err == nil {
		return g.primType(prim)
	}
	return nil
}

// resolveTypes converts all JSON type defs into ResolvedType.
func (g *Generator) resolveTypes() error {
	g.types = make([]ResolvedType, len(g.doc.Types))
	g.primTypes = make(map[string]*ResolvedType)
	for i, td := range g.doc.Types {
		rt, err := g.resolveTypeDef(td)
		if err != nil {
			return fmt.Errorf("type[%d]: %w", i, err)
		}
		rt.Idx = i
		// Parse owner interface once and cache it.
		rt.OwnerInterface = -1
		if td.Owner != nil {
			var owner struct {
				Interface *int `json:"interface"`
			}
			if err := json.Unmarshal(td.Owner, &owner); err == nil && owner.Interface != nil {
				rt.OwnerInterface = *owner.Interface
			}
		}
		g.types[i] = rt
	}
	return nil
}

func (g *Generator) resolveTypeDef(td TypeDef) (ResolvedType, error) {
	var rt ResolvedType
	if td.Name != nil {
		rt.Name = *td.Name
	}

	// Kind can be a string or an object.
	var kindStr string
	if err := json.Unmarshal(td.Kind, &kindStr); err == nil {
		// Simple kind like "resource"
		if kindStr == "resource" {
			rt.Kind = KindResource
			return rt, nil
		}
		return rt, fmt.Errorf("unknown simple kind: %q", kindStr)
	}

	var kindObj map[string]json.RawMessage
	if err := json.Unmarshal(td.Kind, &kindObj); err != nil {
		return rt, fmt.Errorf("cannot parse kind: %s", string(td.Kind))
	}

	if raw, ok := kindObj["type"]; ok {
		rt.Kind = KindTypeAlias
		rt.Alias = g.resolveTypeRef(raw)
		return rt, nil
	}

	if raw, ok := kindObj["record"]; ok {
		rt.Kind = KindRecord
		var rec struct {
			Fields []struct {
				Name string          `json:"name"`
				Type json.RawMessage `json:"type"`
			} `json:"fields"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return ResolvedType{}, fmt.Errorf("record: %w", err)
		}
		for _, f := range rec.Fields {
			rt.Fields = append(rt.Fields, Field{Name: f.Name, Type: g.resolveTypeRef(f.Type)})
		}
		return rt, nil
	}

	if raw, ok := kindObj["tuple"]; ok {
		rt.Kind = KindTuple
		var tup struct {
			Types []json.RawMessage `json:"types"`
		}
		if err := json.Unmarshal(raw, &tup); err != nil {
			return ResolvedType{}, fmt.Errorf("tuple: %w", err)
		}
		for i, t := range tup.Types {
			rt.Fields = append(rt.Fields, Field{Name: fmt.Sprintf("f%d", i), Type: g.resolveTypeRef(t)})
		}
		return rt, nil
	}

	if raw, ok := kindObj["variant"]; ok {
		rt.Kind = KindVariant
		var v struct {
			Cases []struct {
				Name string          `json:"name"`
				Type json.RawMessage `json:"type"`
			} `json:"cases"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return ResolvedType{}, fmt.Errorf("variant: %w", err)
		}
		for _, c := range v.Cases {
			rt.Cases = append(rt.Cases, Case{Name: c.Name, Type: g.resolveTypeRef(c.Type)})
		}
		return rt, nil
	}

	if raw, ok := kindObj["enum"]; ok {
		rt.Kind = KindEnum
		var e struct {
			Cases []struct {
				Name string `json:"name"`
			} `json:"cases"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return ResolvedType{}, fmt.Errorf("enum: %w", err)
		}
		for _, c := range e.Cases {
			rt.Cases = append(rt.Cases, Case{Name: c.Name})
		}
		return rt, nil
	}

	if raw, ok := kindObj["flags"]; ok {
		rt.Kind = KindFlags
		var f struct {
			Flags []struct {
				Name string `json:"name"`
			} `json:"flags"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return ResolvedType{}, fmt.Errorf("flags: %w", err)
		}
		for _, fl := range f.Flags {
			rt.Flags = append(rt.Flags, fl.Name)
		}
		return rt, nil
	}

	if raw, ok := kindObj["option"]; ok {
		rt.Kind = KindOption
		rt.Inner = g.resolveTypeRef(raw)
		return rt, nil
	}

	if raw, ok := kindObj["result"]; ok {
		rt.Kind = KindResult
		var res struct {
			Ok  json.RawMessage `json:"ok"`
			Err json.RawMessage `json:"err"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return ResolvedType{}, fmt.Errorf("result: %w", err)
		}
		rt.Ok = g.resolveTypeRef(res.Ok)
		rt.Err = g.resolveTypeRef(res.Err)
		return rt, nil
	}

	if raw, ok := kindObj["list"]; ok {
		rt.Kind = KindList
		rt.Inner = g.resolveTypeRef(raw)
		return rt, nil
	}

	if raw, ok := kindObj["handle"]; ok {
		rt.Kind = KindHandle
		var h map[string]int
		if err := json.Unmarshal(raw, &h); err != nil {
			return ResolvedType{}, fmt.Errorf("handle: %w", err)
		}
		if v, ok := h["own"]; ok {
			rt.HandleOwn = true
			rt.HandleTarget = &g.types[v]
		} else if v, ok := h["borrow"]; ok {
			rt.HandleOwn = false
			rt.HandleTarget = &g.types[v]
		}
		return rt, nil
	}

	if raw, ok := kindObj["stream"]; ok {
		rt.Kind = KindStream
		rt.Inner = g.resolveTypeRef(raw)
		return rt, nil
	}

	if raw, ok := kindObj["future"]; ok {
		rt.Kind = KindFuture
		rt.Inner = g.resolveTypeRef(raw)
		return rt, nil
	}

	return rt, fmt.Errorf("unknown kind object: %s", string(td.Kind))
}

// computeTypeRenames detects name collisions between types and functions
// across all specs and populates g.typeRenames.
func (g *Generator) computeTypeRenames() {
	funcGoNames := make(map[string]bool)
	for i := range g.specs {
		g.prefix = g.specs[i].Prefix
		iface := g.doc.Interfaces[g.specs[i].IfaceIdx]
		for _, fn := range iface.Functions {
			rf := g.resolveFunc(fn)
			funcGoNames[g.goFuncName(rf)] = true
		}
	}
	g.typeRenames = make(map[*ResolvedType]string)
	for i := range g.specs {
		iface := g.doc.Interfaces[g.specs[i].IfaceIdx]
		for _, typeIdx := range iface.Types {
			rt := &g.types[typeIdx]
			if !g.isOwnedByInterface(rt, g.specs[i].IfaceIdx) {
				continue
			}
			goName := witToGoName(rt.Name)
			if funcGoNames[goName] {
				g.typeRenames[rt] = goName + "T"
			}
		}
	}
}

// generateAll generates all files from the specs.
func (g *Generator) generateAll() error {
	g.generatedTypes = make(map[string]bool)
	g.generatedResults = make(map[string]bool)
	g.computeTypeRenames()
	for i := range g.specs {
		if err := g.generateFile(&g.specs[i]); err != nil {
			return fmt.Errorf("%s: %w", g.specs[i].Filename, err)
		}
	}
	return nil
}

// generateFile generates a single output file for the given spec.
func (g *Generator) generateFile(spec *GenSpec) error {
	// Set per-file state from spec.
	g.ifaceName = spec.IfaceName
	g.ifaceIdx = spec.IfaceIdx
	g.prefix = spec.Prefix
	g.needsWasiImport = false
	g.needsUnsafe = false
	g.fileImports = make(map[string]string)

	iface := g.doc.Interfaces[g.ifaceIdx]

	// Generate the body first so we know which imports are needed.
	var body strings.Builder

	// Sort type and function names for deterministic output.
	typeNames := make([]string, 0, len(iface.Types))
	for name := range iface.Types {
		typeNames = append(typeNames, name)
	}
	sort.Strings(typeNames)

	funcNames := make([]string, 0, len(iface.Functions))
	for name := range iface.Functions {
		funcNames = append(funcNames, name)
	}
	sort.Strings(funcNames)

	// Generate local copies of foreign types referenced by this interface.
	foreignTypes := g.collectForeignTypes()
	for _, rt := range foreignTypes {
		g.generateType(&body, rt)
	}

	// Generate named types owned by this interface.
	for _, name := range typeNames {
		typeIdx := iface.Types[name]
		rt := &g.types[typeIdx]
		if !g.isOwnedByInterface(rt, g.ifaceIdx) {
			continue
		}
		g.generateType(&body, rt)
	}

	// Generate result types (may set needsWasiImport).
	for _, name := range funcNames {
		fn := iface.Functions[name]
		rf := g.resolveFunc(fn)
		if rf.Result != nil {
			resultRT := rf.Result
			// Unwrap future<result<...>> to find the inner result type.
			if rt := g.deref(resultRT); rt.Kind == KindFuture && rt.Inner != nil {
				resultRT = rt.Inner
			}
			g.maybeGenerateResultStruct(&body, resultRT)
		}
	}

	// Generate resource drop functions as methods.
	for _, name := range typeNames {
		typeIdx := iface.Types[name]
		rt := &g.types[typeIdx]
		if rt.Kind == KindResource && g.isOwnedByInterface(rt, g.ifaceIdx) {
			goName := g.resourceGoName(rt)
			recv := g.receiverName(rt)
			modulePath := g.modulePath(iface)
			rawName := unexport(goName) + "Drop"
			fmt.Fprintf(&body, "//go:wasmimport %s [resource-drop]%s\n", modulePath, rt.Name)
			fmt.Fprintf(&body, "func %s(handle int32)\n\n", rawName)
			fmt.Fprintf(&body, "func (%s %s) Drop() { %s(int32(%s)) }\n\n", recv, goName, rawName, recv)
		}
	}

	// Generate function declarations.
	for _, name := range funcNames {
		fn := iface.Functions[name]
		g.generateFunc(&body, fn, iface)
	}

	// Generate stream/future builtins.
	for _, name := range funcNames {
		fn := iface.Functions[name]
		g.generateStreamFutureBuiltins(&body, fn, iface)
	}

	// Generate stream/future vtable instances and composites.
	for _, name := range funcNames {
		fn := iface.Functions[name]
		g.generateStreamOps(&body, fn, iface)
	}

	// Now write the header with correct imports, then the body.
	var out strings.Builder
	fmt.Fprintf(&out, "// Code generated by witgen from %s. DO NOT EDIT.\n\n", g.ifaceName)
	if g.buildTag != "" {
		fmt.Fprintf(&out, "//go:build %s\n\n", g.buildTag)
	}
	fmt.Fprintf(&out, "package %s\n\n", g.pkgName)
	hasImports := g.needsWasiImport || g.needsUnsafe || len(g.fileImports) > 0
	if hasImports {
		fmt.Fprintf(&out, "import (\n")
		if g.needsWasiImport {
			fmt.Fprintf(&out, "\twasi \"internal/wasi\"\n")
		}
		if g.needsUnsafe {
			fmt.Fprintf(&out, "\t\"unsafe\"\n")
		}
		// Cross-package imports from -import flag.
		for goImport, alias := range g.fileImports {
			fmt.Fprintf(&out, "\t%s %q\n", alias, goImport)
		}
		fmt.Fprintf(&out, ")\n\n")
	}
	out.WriteString(body.String())

	// Format and write output file.
	outPath := filepath.Join(g.outDir, spec.Filename)
	formatted, err := format.Source([]byte(out.String()))
	if err != nil {
		return fmt.Errorf("formatting %s: %w", spec.Filename, err)
	}
	if err := os.WriteFile(outPath, formatted, 0666); err != nil {
		return err
	}
	return nil
}

// isTypeAvailable checks if a type is available (defined or importable) in the
// current package. Checks all specs in the batch and import mappings.
func (g *Generator) isTypeAvailable(rt *ResolvedType) bool {
	if rt == nil {
		return true
	}
	if rt.Kind == KindTypeAlias && rt.Alias != nil {
		return g.isTypeAvailable(rt.Alias)
	}
	if rt.Kind == KindPrimitive {
		return true
	}
	if rt.Name != "" {
		for _, spec := range g.specs {
			if g.isOwnedByInterface(rt, spec.IfaceIdx) {
				return true
			}
		}
		// Check if available via import.
		if _, ok := g.importableType(rt); ok {
			return true
		}
		return false
	}
	return true
}

// importableType checks if a type can be referenced via an import spec.
// If so, it returns the qualified Go name (e.g., "clocks.Instant") and marks
// the import as used for the current file. Returns ("", false) if no
// import mapping covers this type.
func (g *Generator) importableType(rt *ResolvedType) (string, bool) {
	if len(g.importSpecs) == 0 {
		return "", false
	}
	if rt.Kind == KindTypeAlias && rt.Alias != nil {
		return g.importableType(rt.Alias)
	}
	if rt.Name == "" {
		return "", false
	}
	ownerPkg := g.ownerPackage(rt)
	if ownerPkg < 0 {
		return "", false
	}
	pkgName := g.doc.Packages[ownerPkg].Name
	for _, imp := range g.importSpecs {
		if imp.WITPkg == pkgName {
			goName := witToGoName(rt.Name)
			if g.fileImports != nil {
				g.fileImports[imp.GoImport] = imp.GoAlias
			}
			return imp.GoAlias + "." + goName, true
		}
	}
	return "", false
}

// isOwnedByInterface checks if a type is owned by the given interface index.
func (g *Generator) isOwnedByInterface(rt *ResolvedType, ifaceIdx int) bool {
	return rt.OwnerInterface == ifaceIdx
}

// ownerPackage returns the WIT package index of the interface that owns a type.
// Returns -1 if the owner cannot be determined.
func (g *Generator) ownerPackage(rt *ResolvedType) int {
	if rt == nil || rt.OwnerInterface < 0 {
		return -1
	}
	return g.doc.Interfaces[rt.OwnerInterface].Package
}

// collectForeignTypes walks the types used by this interface and collects
// type pointers that are referenced but not owned by any interface in the batch.
// These need local type definitions so records/accessors can use them.
func (g *Generator) collectForeignTypes() []*ResolvedType {
	seen := make(map[*ResolvedType]bool)
	generated := make(map[*ResolvedType]bool) // track which types will be generated locally
	var foreign []*ResolvedType

	// Mark all types that are owned by any interface in the batch.
	for _, spec := range g.specs {
		iface := g.doc.Interfaces[spec.IfaceIdx]
		for _, idx := range iface.Types {
			rt := &g.types[idx]
			if g.isOwnedByInterface(rt, spec.IfaceIdx) {
				generated[rt] = true
			}
		}
	}

	// Get the current interface's WIT package index.
	currentPkg := g.doc.Interfaces[g.ifaceIdx].Package

	var walk func(rt *ResolvedType)
	walk = func(rt *ResolvedType) {
		if rt == nil || seen[rt] {
			return
		}
		seen[rt] = true

		// Follow aliases — we care about the actual type, not the alias.
		if rt.Kind == KindTypeAlias && rt.Alias != nil {
			walk(rt.Alias)
			return
		}

		// If this is a named type that won't be generated locally, collect it.
		// Only collect types from a DIFFERENT WIT package — types from sibling
		// interfaces in the same package are already generated by other witgen runs.
		// Skip TypeAlias types — they are already re-exported by the local `use` statement.
		// Skip types that have an import mapping — they'll be referenced via import.
		if rt.Name != "" && !generated[rt] && rt.Kind != KindTypeAlias {
			ownerPkg := g.ownerPackage(rt)
			if ownerPkg >= 0 && ownerPkg != currentPkg {
				if _, importable := g.importableType(rt); !importable {
					foreign = append(foreign, rt)
					generated[rt] = true
				}
			}
		}

		// Walk nested types.
		switch rt.Kind {
		case KindRecord, KindTuple:
			for _, f := range rt.Fields {
				walk(f.Type)
			}
		case KindOption:
			walk(rt.Inner)
		case KindList:
			walk(rt.Inner)
		case KindVariant:
			for _, c := range rt.Cases {
				walk(c.Type)
			}
		case KindResult:
			walk(rt.Ok)
			walk(rt.Err)
		}
	}

	// Walk all types in this interface (including aliased foreign types).
	iface := g.doc.Interfaces[g.ifaceIdx]
	for _, typeIdx := range iface.Types {
		walk(&g.types[typeIdx])
	}
	// Walk all function result types.
	for _, fn := range iface.Functions {
		rf := g.resolveFunc(fn)
		walk(rf.Result)
	}

	return foreign
}
