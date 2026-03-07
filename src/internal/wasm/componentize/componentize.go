// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package componentize converts core wasm modules into wasm components
// for the wasip3/wasm32 target. It replaces the need for wasm-tools
// component embed + wasm-tools component new.
package componentize

import (
	"fmt"
	"strings"
)

const wasiVersion = "0.3.0-rc-2026-02-09"

// Component section IDs.
const (
	secCustom        byte = 0x00
	secCoreModule    byte = 0x01
	secCoreInstance  byte = 0x02
	secAlias         byte = 0x06
	secComponentType byte = 0x07
	secCanon         byte = 0x08
	secImport        byte = 0x0a
	secExport        byte = 0x0b
	secComponent     byte = 0x04
	secCompInstance  byte = 0x05
)

// Canonical function opcodes.
const (
	canonLift               byte = 0x00
	canonLower              byte = 0x01
	canonResourceDrop       byte = 0x03
	canonSubtaskCancel      byte = 0x06
	canonTaskReturn         byte = 0x09
	canonContextGet         byte = 0x0a
	canonContextSet         byte = 0x0b
	canonSubtaskDrop        byte = 0x0d
	canonStreamNew          byte = 0x0e
	canonStreamRead         byte = 0x0f
	canonStreamWrite        byte = 0x10
	canonStreamDropReadable byte = 0x13
	canonStreamDropWritable byte = 0x14
	canonFutureRead         byte = 0x16
	canonFutureDropReadable byte = 0x1a
	canonWaitableSetNew     byte = 0x1f
	canonWaitableSetPoll    byte = 0x21
	canonWaitableJoin       byte = 0x23
)

// Canonical options.
const (
	optUTF8     byte = 0x00
	optMemory   byte = 0x03
	optRealloc  byte = 0x04
	optAsync    byte = 0x06
	optCallback byte = 0x07
)

// Embed appends the component-type custom section to a core wasm module.
// The component-type data is generated from the WIT world definition.
func Embed(coreModule []byte) []byte {
	ctData := buildComponentTypeBlob()
	return appendCustomSection(coreModule, "component-type", ctData)
}

// buildComponentTypeBlob generates the component-type custom section data.
// This is a nested component binary containing the world's type info.
func buildComponentTypeBlob() []byte {
	var b []byte
	// Component magic header
	b = append(b, 0x00, 0x61, 0x73, 0x6d, 0x0d, 0x00, 0x01, 0x00)

	// Custom section: wit-component-encoding
	encData := []byte{0x04, 0x00} // version=4, encoding=utf8
	b = appendSection(b, secCustom, buildCustomSectionPayload("wit-component-encoding", encData))

	// Build the world's component type
	worldType := buildWorldComponentType()
	var typeSec []byte
	typeSec = appendUleb128(typeSec, 1) // count
	typeSec = append(typeSec, worldType...)
	b = appendSection(b, secComponentType, typeSec)

	// Export section: export the type
	var exportSec []byte
	exportSec = appendUleb128(exportSec, 1)
	exportSec = append(exportSec, 0x00) // name kind: kebab
	exportSec = appendName(exportSec, "command")
	exportSec = append(exportSec, 0x03)     // sort: type
	exportSec = appendUleb128(exportSec, 0) // index
	exportSec = append(exportSec, 0x00)     // no type ascription
	b = appendSection(b, secExport, exportSec)

	// Producers custom section
	b = appendSection(b, secCustom, buildCustomSectionPayload("producers",
		buildProducersPayload()))

	return b
}

// buildWorldComponentType encodes the entire world as a component type (0x41).
func buildWorldComponentType() []byte {
	witDir := defaultWitDir()
	ifaces, world, _, err := buildWorldFromWIT(witDir, "command")
	if err != nil {
		panic(fmt.Sprintf("building world from WIT: %v", err))
	}

	var items []byte
	var count uint32

	// Track type and instance indices within the component type
	var typeIdx, instanceIdx uint32

	for _, iface := range ifaces {
		if len(iface.items) == 0 {
			continue
		}

		// Emit instance type inline
		instType := encodeInstanceType(iface.items)
		items = append(items, 0x01) // type item
		items = append(items, instType...)
		instTypeIdx := typeIdx
		typeIdx++
		count++

		// Emit import
		items = append(items, 0x03) // import item (0x03 in component type)
		items = append(items, 0x00) // name kind
		items = appendName(items, iface.name)
		items = append(items, 0x05) // extern: instance
		items = appendUleb128(items, instTypeIdx)
		instanceIdx++
		count++

		instIdx := instanceIdx - 1

		// Emit aliases
		for _, alias := range iface.aliases {
			items = append(items, 0x02) // alias item
			items = append(items, 0x03) // sort: type
			items = append(items, 0x00) // kind: instance export
			items = appendUleb128(items, instIdx)
			items = appendName(items, alias.exportName)
			typeIdx++
			count++
		}
	}

	// Add world exports (e.g. wasi:cli/run)
	for _, exp := range world.Exports {
		expName := exp.InterfaceName
		if !strings.Contains(expName, ":") {
			// Need to resolve local name to qualified
			// Find the world's package
			for _, pkg := range []*WitPackage{} {
				_ = pkg // will be resolved below
			}
		}
		// For now, resolve the export interface
		qualifiedName := expName
		if !strings.Contains(qualifiedName, ":") {
			// Look up in packages - for the Go world, exports reference interfaces
			// from other packages (like wasi:cli/run)
			// The world's own package is go:wasip3-proto, but the export is wasi:cli/run
			// which is already qualified in the world definition.
			continue
		}

		// Build the export instance type
		// For wasi:cli/run: instance type with async func() -> result
		items = append(items, 0x01) // type item: instance type
		items = append(items, ttInstanceType)
		items = appendUleb128(items, 2) // 2 items in instance type
		// result type
		items = append(items, 0x01)                  // type def
		items = append(items, cvtResult, 0x00, 0x00) // result<_, _>
		// func type
		items = append(items, 0x01)        // type def
		items = append(items, ttFuncAsync) // async func
		items = appendUleb128(items, 0)    // 0 params
		items = append(items, 0x00)        // has result
		items = appendUleb128(items, 0)    // type ref 0 (result)
		exportTypeIdx := typeIdx
		typeIdx++
		count++

		// Export
		items = append(items, 0x04) // export
		items = append(items, 0x00) // name kind
		items = appendName(items, qualifiedName)
		items = append(items, 0x05) // extern: instance
		items = appendUleb128(items, exportTypeIdx)
		count++
	}

	var result []byte
	result = append(result, ttComponentType)
	result = appendUleb128(result, count)
	result = append(result, items...)
	return result
}

// New takes a core module with the component-type custom section and produces
// a full wasm component binary.
func New(embeddedModule []byte) ([]byte, error) {
	coreModule, err := stripCustomSection(embeddedModule, "component-type")
	if err != nil {
		return nil, fmt.Errorf("stripping component-type: %w", err)
	}
	mod, err := ParseCoreModule(coreModule)
	if err != nil {
		return nil, fmt.Errorf("parsing core module: %w", err)
	}
	// Deduplicate import names within each module (required by component model).
	// Append " [vN]" suffix to duplicate names.
	deduplicateImports(mod)
	return newComponent(coreModule, mod)
}

// deduplicateImports renames duplicate import names within each module
// by appending " [vN]" suffixes, as required by the component model.
func deduplicateImports(mod *CoreModule) {
	seen := make(map[string]int) // "module:name" -> count
	for i := range mod.Imports {
		key := mod.Imports[i].Module + ":" + mod.Imports[i].Name
		count := seen[key]
		seen[key] = count + 1
		if count > 0 {
			mod.Imports[i].Name = fmt.Sprintf("%s [v%d]", mod.Imports[i].Name, count+1)
		}
	}
}

// deduplicateCoreModule rewrites the core wasm binary's import section
// so that the import names match mod.Imports (which have been deduplicated).
func deduplicateCoreModule(data []byte, mod *CoreModule) []byte {
	// Walk through sections, find the import section, rebuild it.
	out := make([]byte, 0, len(data)+256)
	out = append(out, data[:8]...) // magic + version

	offset := 8
	importIdx := 0

	for offset < len(data) {
		sectionID := data[offset]
		sectionStart := offset
		offset++

		sectionLen, newOffset, err := readUleb128(data, offset)
		if err != nil {
			// Shouldn't happen since we already parsed it; return original.
			return data
		}
		offset = newOffset
		sectionEnd := offset + int(sectionLen)
		if sectionEnd > len(data) {
			return data
		}

		if sectionID != sectionImport {
			// Copy section verbatim (ID + length + body).
			out = append(out, data[sectionStart:sectionEnd]...)
			offset = sectionEnd
			continue
		}

		// Rebuild the import section with deduplicated names.
		var body []byte
		body = appendUleb128(body, uint32(len(mod.Imports)))

		pos := offset // position within original import section body
		count, pos, err := readUleb128(data, pos)
		if err != nil {
			return data
		}

		for i := uint32(0); i < count; i++ {
			// Skip original module name
			_, pos, err = readName(data, pos)
			if err != nil {
				return data
			}
			// Skip original field name
			_, pos, err = readName(data, pos)
			if err != nil {
				return data
			}

			// Record start of the kind+descriptor bytes
			descStart := pos

			// Skip the descriptor to find the end
			kind := data[pos]
			pos++
			switch kind {
			case importFunc:
				_, pos, err = readUleb128(data, pos)
				if err != nil {
					return data
				}
			case importTable:
				pos++ // reftype
				flags := data[pos]
				pos++
				_, pos, err = readUleb128(data, pos) // min
				if err != nil {
					return data
				}
				if flags&1 != 0 {
					_, pos, err = readUleb128(data, pos) // max
					if err != nil {
						return data
					}
				}
			case importMemory:
				flags := data[pos]
				pos++
				_, pos, err = readUleb128(data, pos)
				if err != nil {
					return data
				}
				if flags&1 != 0 {
					_, pos, err = readUleb128(data, pos)
					if err != nil {
						return data
					}
				}
			case importGlobal:
				pos++ // valtype
				pos++ // mut
			default:
				return data
			}

			// Write renamed import: module name + field name + original descriptor
			body = appendName(body, mod.Imports[importIdx].Module)
			body = appendName(body, mod.Imports[importIdx].Name)
			body = append(body, data[descStart:pos]...)
			importIdx++
		}

		// Write section: ID + length + body
		out = append(out, sectionImport)
		out = appendUleb128(out, uint32(len(body)))
		out = append(out, body...)

		offset = sectionEnd
	}

	return out
}

func newComponent(coreModule []byte, mod *CoreModule) ([]byte, error) {
	c := &compBuilder{mod: mod}
	return c.build(coreModule)
}

type compBuilder struct {
	mod *CoreModule
	out []byte

	// Index spaces
	coreFunc      uint32
	coreInstance  uint32
	coreMemory    uint32
	coreTable     uint32
	compFunc      uint32
	compType      uint32
	compInstance  uint32
	compModule    uint32
	compComponent uint32

	// Key indices resolved during build
	shimInstance uint32
	mainInstance uint32
	memoryIdx    uint32
	reallocIdx   uint32
	shimTableIdx uint32

	// Preamble info
	preamble      *preambleBuilder
	compInstances map[string]uint32 // interface name -> component instance index

	// Indirect lowerings
	indirects []indirect
}

type indirect struct {
	shimIdx int
	ft      FuncType
	emitFn  func() // called post-instantiation to emit the real canon lower
}

func (c *compBuilder) build(coreModule []byte) ([]byte, error) {
	c.out = make([]byte, 0, len(coreModule)+16384)
	c.compInstances = make(map[string]uint32)

	// Component header
	c.out = append(c.out, 0x00, 0x61, 0x73, 0x6d, 0x0d, 0x00, 0x01, 0x00)

	// 1. Preamble: type + import + alias sections for the world
	preambleBytes, pb := buildPreamble()
	c.out = append(c.out, preambleBytes...)
	c.preamble = pb
	c.compType = pb.typeIdx
	c.compInstance = pb.instanceIdx

	// Copy instance map
	for k, v := range pb.instanceMap {
		c.compInstances[k] = v
	}

	// 2. Core module section (with deduplicated import names)
	c.emitCoreModule(deduplicateCoreModule(coreModule, c.mod))
	mainModIdx := c.compModule - 1

	// 3. Analyze imports, build shim
	c.analyzeAndBuildShim()
	shimModIdx := c.compModule - 1

	// 4. Fixup module
	c.emitFixupModule()
	fixupModIdx := c.compModule - 1

	// 5. Instantiate shim (no args)
	c.emitCoreInstantiate(shimModIdx, nil)
	c.shimInstance = c.coreInstance - 1

	// 6. Pre-instantiation wiring: for each import group,
	//    emit canon ops + core instances
	if err := c.emitImportWiring(); err != nil {
		return nil, err
	}

	// 7. Instantiate main module
	c.emitMainInstantiate(mainModIdx)
	c.mainInstance = c.coreInstance - 1

	// 8. Alias memory, table, realloc from main/shim instances
	c.aliasCoreFuncExport(c.mainInstance, "memory", []byte{0x00, 0x02}) // core memory sort
	c.memoryIdx = c.coreMemory - 1

	c.aliasCoreFuncExport(c.shimInstance, "$imports", []byte{0x00, 0x01}) // core table sort
	c.shimTableIdx = c.coreTable - 1

	c.aliasCoreFuncExport(c.mainInstance, "cabi_realloc", []byte{0x00, 0x00}) // core func sort
	c.reallocIdx = c.coreFunc - 1

	// 9. Post-instantiation: emit real canon lowers for indirect imports
	for i, ind := range c.indirects {
		if ind.emitFn == nil {
			panic(fmt.Sprintf("indirect %d has nil emitFn (shimIdx=%d)", i, ind.shimIdx))
		}
		ind.emitFn()
	}

	// 10. Fixup instantiation
	c.emitFixupInstantiation(fixupModIdx)

	// 11. Export wasi:cli/run
	c.emitRunExport()

	// 12. Producers section
	c.emitSection(secCustom, buildCustomSectionPayload("producers", buildProducersPayload()))

	return c.out, nil
}

func (c *compBuilder) emitSection(id byte, payload []byte) {
	c.out = appendSection(c.out, id, payload)
}

func (c *compBuilder) emitCoreModule(data []byte) {
	c.out = append(c.out, secCoreModule)
	c.out = appendUleb128(c.out, uint32(len(data)))
	c.out = append(c.out, data...)
	c.compModule++
}

func (c *compBuilder) analyzeAndBuildShim() {
	// Group imports by module name and classify each one
	groups := c.groupImports()

	// Determine which imports need indirect lowering (need memory/realloc)
	for _, g := range groups {
		for _, imp := range g.imports {
			if needsIndirect(imp) {
				ft := c.mod.Types[imp.TypeIdx]
				idx := len(c.indirects)
				c.indirects = append(c.indirects, indirect{
					shimIdx: idx,
					ft:      ft,
				})
			}
		}
	}

	// Build shim module
	c.buildAndEmitShimModule()
}

type importGroup struct {
	module  string
	imports []Import
}

func (c *compBuilder) groupImports() []importGroup {
	seen := make(map[string]int)
	var groups []importGroup
	for _, imp := range c.mod.Imports {
		if imp.Kind != importFunc {
			continue
		}
		idx, ok := seen[imp.Module]
		if !ok {
			idx = len(groups)
			seen[imp.Module] = idx
			groups = append(groups, importGroup{module: imp.Module})
		}
		groups[idx].imports = append(groups[idx].imports, imp)
	}
	return groups
}

// needsIndirect returns true if this import requires memory/realloc for canon lower.
func needsIndirect(imp Import) bool {
	name := imp.Name
	// Async-lowered functions need memory — but $root module handles its own
	// canon builtins directly (e.g. [async-lower][subtask-cancel]), not via indirect.
	if strings.HasPrefix(name, "[async-lower]") && imp.Module != "$root" {
		return true
	}
	// Stream/future operations that move data need memory+async
	if strings.HasPrefix(name, "[stream-write-") || strings.HasPrefix(name, "[stream-read-") ||
		strings.HasPrefix(name, "[future-read-") {
		// Check if it also has [async-lower] prefix
		return true
	}
	// Regular functions that take/return strings/lists need memory+realloc
	// These are identified by the interface they come from:
	// - get-arguments, get-environment (return list<string>)
	// - get-directories (return list<tuple<own<desc>, string>>)
	// - get-initial-cwd (return option<string>)
	mod := imp.Module
	switch {
	case strings.Contains(mod, "cli/environment"):
		if name == "get-arguments" || name == "get-environment" || name == "get-initial-cwd" {
			return true
		}
	case strings.Contains(mod, "filesystem/preopens"):
		if name == "get-directories" {
			return true
		}
	case strings.Contains(mod, "clocks/system-clock"):
		if name == "now" {
			return true
		}
	case strings.Contains(mod, "sockets/types"):
		if strings.HasPrefix(name, "[async-lower]") {
			return true
		}
	case strings.Contains(mod, "exec/exec"):
		if strings.HasPrefix(name, "[async-lower]") {
			return true
		}
	}
	// [waitable-set-poll] needs memory
	if name == "[waitable-set-poll]" {
		return true
	}
	return false
}

func (c *compBuilder) buildAndEmitShimModule() {
	if len(c.indirects) == 0 {
		// Still need an empty shim for the table
		c.emitCoreModule(buildEmptyShimModule())
		return
	}

	// Collect unique function types used by indirect imports
	typeMap := make(map[string]uint32) // encoded type -> type index
	var types []FuncType

	for i := range c.indirects {
		ft := c.indirects[i].ft
		key := encodeFuncTypeKey(ft)
		if _, ok := typeMap[key]; !ok {
			typeMap[key] = uint32(len(types))
			types = append(types, ft)
		}
	}

	var mod []byte
	// Wasm module header
	mod = append(mod, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)

	// Type section
	var typeSec []byte
	typeSec = appendUleb128(typeSec, uint32(len(types)))
	for _, ft := range types {
		typeSec = append(typeSec, 0x60) // func type
		typeSec = appendUleb128(typeSec, uint32(len(ft.Params)))
		typeSec = append(typeSec, ft.Params...)
		typeSec = appendUleb128(typeSec, uint32(len(ft.Results)))
		typeSec = append(typeSec, ft.Results...)
	}
	mod = appendSection(mod, 0x01, typeSec)

	// Function section (0x03): one function per indirect
	var funcSec []byte
	funcSec = appendUleb128(funcSec, uint32(len(c.indirects)))
	for i := range c.indirects {
		key := encodeFuncTypeKey(c.indirects[i].ft)
		funcSec = appendUleb128(funcSec, typeMap[key])
	}
	mod = appendSection(mod, 0x03, funcSec)

	// Table section (0x04): one table with N entries
	var tableSec []byte
	tableSec = appendUleb128(tableSec, 1) // 1 table
	tableSec = append(tableSec, 0x70)     // funcref
	tableSec = append(tableSec, 0x00)     // limits: no max
	tableSec = appendUleb128(tableSec, uint32(len(c.indirects)))
	mod = appendSection(mod, 0x04, tableSec)

	// Export section: table as "$imports", each func as "0", "1", ...
	var exportSec []byte
	exportSec = appendUleb128(exportSec, uint32(1+len(c.indirects)))
	// Table export
	exportSec = appendName(exportSec, "$imports")
	exportSec = append(exportSec, 0x01) // table
	exportSec = appendUleb128(exportSec, 0)
	// Function exports
	for i := range c.indirects {
		exportSec = appendName(exportSec, fmt.Sprintf("%d", i))
		exportSec = append(exportSec, 0x00) // func
		exportSec = appendUleb128(exportSec, uint32(i))
	}
	mod = appendSection(mod, 0x07, exportSec)

	// Code section: each function does local.get*, i32.const idx, call_indirect
	var codeSec []byte
	codeSec = appendUleb128(codeSec, uint32(len(c.indirects)))
	for i := range c.indirects {
		ft := c.indirects[i].ft
		key := encodeFuncTypeKey(ft)
		typeIdx := typeMap[key]

		var body []byte
		body = appendUleb128(body, 0) // no locals
		for j := range ft.Params {
			body = append(body, 0x20) // local.get
			body = appendUleb128(body, uint32(j))
		}
		body = append(body, 0x41) // i32.const
		body = appendSleb128(body, int32(i))
		body = append(body, 0x11) // call_indirect
		body = appendUleb128(body, typeIdx)
		body = appendUleb128(body, 0) // table 0
		body = append(body, 0x0b)     // end

		codeSec = appendUleb128(codeSec, uint32(len(body)))
		codeSec = append(codeSec, body...)
	}
	mod = appendSection(mod, 0x0a, codeSec)

	c.emitCoreModule(mod)
}

func buildEmptyShimModule() []byte {
	var mod []byte
	mod = append(mod, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	// Table
	var tableSec []byte
	tableSec = appendUleb128(tableSec, 1)
	tableSec = append(tableSec, 0x70, 0x00)
	tableSec = appendUleb128(tableSec, 0)
	mod = appendSection(mod, 0x04, tableSec)
	// Export table
	var exportSec []byte
	exportSec = appendUleb128(exportSec, 1)
	exportSec = appendName(exportSec, "$imports")
	exportSec = append(exportSec, 0x01)
	exportSec = appendUleb128(exportSec, 0)
	mod = appendSection(mod, 0x07, exportSec)
	return mod
}

func (c *compBuilder) emitFixupModule() {
	if len(c.indirects) == 0 {
		c.emitCoreModule(buildEmptyFixupModule())
		return
	}

	// Collect unique types
	typeMap := make(map[string]uint32)
	var types []FuncType
	for i := range c.indirects {
		ft := c.indirects[i].ft
		key := encodeFuncTypeKey(ft)
		if _, ok := typeMap[key]; !ok {
			typeMap[key] = uint32(len(types))
			types = append(types, ft)
		}
	}

	var mod []byte
	mod = append(mod, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)

	// Type section
	var typeSec []byte
	typeSec = appendUleb128(typeSec, uint32(len(types)))
	for _, ft := range types {
		typeSec = append(typeSec, 0x60)
		typeSec = appendUleb128(typeSec, uint32(len(ft.Params)))
		typeSec = append(typeSec, ft.Params...)
		typeSec = appendUleb128(typeSec, uint32(len(ft.Results)))
		typeSec = append(typeSec, ft.Results...)
	}
	mod = appendSection(mod, 0x01, typeSec)

	// Import section: table + each function
	var importSec []byte
	importSec = appendUleb128(importSec, uint32(1+len(c.indirects)))
	// Table import
	importSec = appendName(importSec, "")
	importSec = appendName(importSec, "$imports")
	importSec = append(importSec, 0x01) // table
	importSec = append(importSec, 0x70) // funcref
	importSec = append(importSec, 0x00) // limits: no max
	importSec = appendUleb128(importSec, uint32(len(c.indirects)))
	// Function imports
	for i := range c.indirects {
		ft := c.indirects[i].ft
		key := encodeFuncTypeKey(ft)
		importSec = appendName(importSec, "")
		importSec = appendName(importSec, fmt.Sprintf("%d", i))
		importSec = append(importSec, 0x00) // func
		importSec = appendUleb128(importSec, typeMap[key])
	}
	mod = appendSection(mod, 0x02, importSec)

	// Element section: active segment populating the table
	var elemSec []byte
	elemSec = appendUleb128(elemSec, 1) // 1 segment
	elemSec = append(elemSec, 0x00)     // active, table 0, offset expr follows
	elemSec = append(elemSec, 0x41)     // i32.const
	elemSec = appendSleb128(elemSec, 0) // offset 0
	elemSec = append(elemSec, 0x0b)     // end
	elemSec = appendUleb128(elemSec, uint32(len(c.indirects)))
	for i := range c.indirects {
		elemSec = appendUleb128(elemSec, uint32(i)) // func index (all are imports)
	}
	mod = appendSection(mod, 0x09, elemSec)

	c.emitCoreModule(mod)
}

func buildEmptyFixupModule() []byte {
	var mod []byte
	mod = append(mod, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	return mod
}

func (c *compBuilder) emitCoreInstantiate(moduleIdx uint32, args []instArg) {
	var payload []byte
	payload = appendUleb128(payload, 1)
	payload = append(payload, 0x00) // instantiate
	payload = appendUleb128(payload, moduleIdx)
	payload = appendUleb128(payload, uint32(len(args)))
	for _, a := range args {
		payload = appendName(payload, a.name)
		payload = append(payload, 0x12) // sort: instance
		payload = appendUleb128(payload, a.instanceIdx)
	}
	c.emitSection(secCoreInstance, payload)
	c.coreInstance++
}

type instArg struct {
	name        string
	instanceIdx uint32
}

func (c *compBuilder) aliasCoreFuncExport(instanceIdx uint32, name string, sortBytes []byte) {
	var payload []byte
	payload = appendUleb128(payload, 1)
	payload = append(payload, sortBytes...) // sort
	payload = append(payload, 0x01)         // kind: core instance export
	payload = appendUleb128(payload, instanceIdx)
	payload = appendName(payload, name)
	c.emitSection(secAlias, payload)
	if len(sortBytes) == 2 && sortBytes[0] == 0x00 && sortBytes[1] == 0x00 {
		c.coreFunc++
	} else if len(sortBytes) == 2 && sortBytes[0] == 0x00 && sortBytes[1] == 0x02 {
		c.coreMemory++
	} else if len(sortBytes) == 2 && sortBytes[0] == 0x00 && sortBytes[1] == 0x01 {
		c.coreTable++
	}
}

// aliasCompExport aliases a func or type from a component instance.
func (c *compBuilder) aliasCompExport(instanceIdx uint32, name string, sort byte) uint32 {
	var payload []byte
	payload = appendUleb128(payload, 1)
	payload = append(payload, sort) // sort: 0x01=func, 0x03=type
	payload = append(payload, 0x00) // kind: instance export
	payload = appendUleb128(payload, instanceIdx)
	payload = appendName(payload, name)
	c.emitSection(secAlias, payload)
	if sort == 0x01 {
		idx := c.compFunc
		c.compFunc++
		return idx
	}
	idx := c.compType
	c.compType++
	return idx
}

func (c *compBuilder) emitCanon(payload []byte) {
	var sec []byte
	sec = appendUleb128(sec, 1)
	sec = append(sec, payload...)
	c.emitSection(secCanon, sec)
	c.coreFunc++
}

func (c *compBuilder) emitCanonLift(payload []byte) {
	var sec []byte
	sec = appendUleb128(sec, 1)
	sec = append(sec, payload...)
	c.emitSection(secCanon, sec)
	c.compFunc++
}

func (c *compBuilder) emitImportWiring() error {
	groups := c.groupImports()
	indirectIdx := 0

	for _, g := range groups {
		if g.module == "$root" {
			c.emitRootWiring(g, &indirectIdx)
			continue
		}
		if strings.HasPrefix(g.module, "[export]") {
			c.emitExportModuleWiring(g)
			continue
		}

		// Interface import group
		c.emitInterfaceWiring(g, &indirectIdx)
	}
	return nil
}

func (c *compBuilder) emitRootWiring(g importGroup, indirectIdx *int) {
	// Built-in canonical operations: waitable-set, subtask, context
	var exports []coreExport

	for _, imp := range g.imports {
		name := imp.Name
		switch {
		case name == "[waitable-set-new]":
			var p []byte
			p = append(p, canonWaitableSetNew)
			c.emitCanon(p)
			exports = append(exports, coreExport{name, c.coreFunc - 1})

		case name == "[waitable-set-poll]":
			// Indirect — needs memory
			shimFuncIdx := c.aliasShimFunc(*indirectIdx)
			exports = append(exports, coreExport{name, shimFuncIdx})
			c.indirects[*indirectIdx].emitFn = func() {
				var p []byte
				p = append(p, canonWaitableSetPoll)
				p = append(p, 0x00) // cancellable = false
				p = appendUleb128(p, c.memoryIdx)
				c.emitCanon(p)
			}
			*indirectIdx++

		case name == "[waitable-join]":
			var p []byte
			p = append(p, canonWaitableJoin)
			c.emitCanon(p)
			exports = append(exports, coreExport{name, c.coreFunc - 1})

		case name == "[async-lower][subtask-cancel]":
			var p []byte
			p = append(p, canonSubtaskCancel)
			p = append(p, 0x01) // async = true
			c.emitCanon(p)
			exports = append(exports, coreExport{name, c.coreFunc - 1})

		case name == "[subtask-drop]":
			var p []byte
			p = append(p, canonSubtaskDrop)
			c.emitCanon(p)
			exports = append(exports, coreExport{name, c.coreFunc - 1})

		case strings.HasPrefix(name, "[context-get-"):
			ctxIdx := parseContextIdx(name)
			var p []byte
			p = append(p, canonContextGet)
			p = append(p, 0x7f) // i32
			p = appendUleb128(p, ctxIdx)
			c.emitCanon(p)
			exports = append(exports, coreExport{name, c.coreFunc - 1})

		case strings.HasPrefix(name, "[context-set-"):
			ctxIdx := parseContextIdx(name)
			var p []byte
			p = append(p, canonContextSet)
			p = append(p, 0x7f) // i32
			p = appendUleb128(p, ctxIdx)
			c.emitCanon(p)
			exports = append(exports, coreExport{name, c.coreFunc - 1})
		}
	}

	c.emitCoreInstanceFromExports(exports)
}

func (c *compBuilder) emitExportModuleWiring(g importGroup) {
	// [export]wasi:cli/run@... — task-return
	var exports []coreExport
	for _, imp := range g.imports {
		if strings.HasPrefix(imp.Name, "[task-return]") {
			funcName := strings.TrimPrefix(imp.Name, "[task-return]")
			// Find the run export type from the preamble
			// task.return for run: the result type is result<>
			// We need to find the right component type index for result<>
			resultTypeIdx := c.emitResultType()
			var p []byte
			p = append(p, canonTaskReturn)
			p = append(p, 0x00) // has result type
			p = appendUleb128(p, resultTypeIdx)
			p = appendUleb128(p, 0) // 0 options
			c.emitCanon(p)
			exports = append(exports, coreExport{imp.Name, c.coreFunc - 1})
			_ = funcName
		}
	}
	c.emitCoreInstanceFromExports(exports)
}

func (c *compBuilder) emitResultType() uint32 {
	// Emit a component type for result<>
	var typeSec []byte
	typeSec = appendUleb128(typeSec, 1)
	typeSec = append(typeSec, cvtResult, 0x00, 0x00) // result<_,_>
	c.emitSection(secComponentType, typeSec)
	idx := c.compType
	c.compType++
	return idx
}

func (c *compBuilder) emitInterfaceWiring(g importGroup, indirectIdx *int) {
	ifaceName := g.module
	compInstIdx, ok := c.compInstances[ifaceName]
	if !ok {
		panic(fmt.Sprintf("interface %q not found in preamble (have: %v)", ifaceName, c.compInstances))
	}

	var exports []coreExport

	for _, imp := range g.imports {
		name := imp.Name
		coreFuncIdx := c.handleImport(imp, ifaceName, compInstIdx, indirectIdx)
		exports = append(exports, coreExport{name, coreFuncIdx})
	}

	c.emitCoreInstanceFromExports(exports)
}

func (c *compBuilder) handleImport(imp Import, ifaceName string, compInstIdx uint32, indirectIdx *int) uint32 {
	name := imp.Name

	// Determine the base function name (strip prefixes and dedup suffixes)
	baseName := name
	// Strip deduplication suffix " [vN]" added by deduplicateImports
	if idx := strings.Index(baseName, " [v"); idx >= 0 {
		baseName = baseName[:idx]
	}
	isAsyncLower := false
	var streamOp string
	var futureOp string

	if strings.HasPrefix(baseName, "[async-lower]") {
		isAsyncLower = true
		baseName = baseName[len("[async-lower]"):]
	}

	if idx := strings.Index(baseName, "[stream-new-"); idx == 0 {
		streamOp = "stream.new"
		baseName = baseName[strings.Index(baseName, "]")+1:]
	} else if idx := strings.Index(baseName, "[stream-write-"); idx == 0 {
		streamOp = "stream.write"
		baseName = baseName[strings.Index(baseName, "]")+1:]
	} else if idx := strings.Index(baseName, "[stream-read-"); idx == 0 {
		streamOp = "stream.read"
		baseName = baseName[strings.Index(baseName, "]")+1:]
	} else if idx := strings.Index(baseName, "[stream-drop-writable-"); idx == 0 {
		streamOp = "stream.drop-writable"
		baseName = baseName[strings.Index(baseName, "]")+1:]
	} else if idx := strings.Index(baseName, "[stream-drop-readable-"); idx == 0 {
		streamOp = "stream.drop-readable"
		baseName = baseName[strings.Index(baseName, "]")+1:]
	} else if idx := strings.Index(baseName, "[future-read-"); idx == 0 {
		futureOp = "future.read"
		baseName = baseName[strings.Index(baseName, "]")+1:]
	} else if idx := strings.Index(baseName, "[future-drop-readable-"); idx == 0 {
		futureOp = "future.drop-readable"
		baseName = baseName[strings.Index(baseName, "]")+1:]
	} else if strings.HasPrefix(baseName, "[resource-drop]") {
		// resource.drop
		typeName := baseName[len("[resource-drop]"):]
		typeIdx := c.aliasCompExport(compInstIdx, typeName, 0x03) // alias type
		var p []byte
		p = append(p, canonResourceDrop)
		p = appendUleb128(p, typeIdx)
		c.emitCanon(p)
		return c.coreFunc - 1
	}

	// Handle stream/future operations
	if streamOp != "" || futureOp != "" {
		return c.handleStreamFutureOp(imp, ifaceName, compInstIdx, streamOp, futureOp, baseName, isAsyncLower, indirectIdx)
	}

	// Regular function: alias from component instance, then canon lower
	if needsIndirect(imp) {
		shimFuncIdx := c.aliasShimFunc(*indirectIdx)
		capturedIdx := *indirectIdx
		c.indirects[capturedIdx].emitFn = func() {
			funcIdx := c.aliasCompExport(compInstIdx, baseName, 0x01)
			var opts []byte
			numOpts := uint32(0)
			if isAsyncLower {
				opts = append(opts, optAsync)
				numOpts++
			}
			if c.needsMemory(imp) {
				opts = append(opts, optMemory)
				opts = appendUleb128(opts, c.memoryIdx)
				numOpts++
			}
			if c.needsRealloc(imp) {
				opts = append(opts, optRealloc)
				opts = appendUleb128(opts, c.reallocIdx)
				numOpts++
				opts = append(opts, optUTF8)
				numOpts++
			}
			var p []byte
			p = append(p, canonLower, 0x00)
			p = appendUleb128(p, funcIdx)
			p = appendUleb128(p, numOpts)
			p = append(p, opts...)
			c.emitCanon(p)
		}
		*indirectIdx++
		return shimFuncIdx
	}

	// Direct lowering
	funcIdx := c.aliasCompExport(compInstIdx, baseName, 0x01)
	var opts []byte
	if isAsyncLower {
		opts = append(opts, optAsync)
	}
	var p []byte
	p = append(p, canonLower, 0x00)
	p = appendUleb128(p, funcIdx)
	p = appendUleb128(p, uint32(len(opts)))
	p = append(p, opts...)
	c.emitCanon(p)
	return c.coreFunc - 1
}

func (c *compBuilder) handleStreamFutureOp(imp Import, ifaceName string, compInstIdx uint32,
	streamOp, futureOp, baseName string, isAsyncLower bool, indirectIdx *int) uint32 {

	// Determine the stream/future type to use
	// For most cases it's stream<u8>; for sockets it might be different
	streamTypeIdx := c.getOrCreateStreamU8Type()

	op := streamOp
	if futureOp != "" {
		op = futureOp
	}

	switch op {
	case "stream.new":
		var p []byte
		p = append(p, canonStreamNew)
		p = appendUleb128(p, streamTypeIdx)
		c.emitCanon(p)
		return c.coreFunc - 1

	case "stream.drop-writable":
		var p []byte
		p = append(p, canonStreamDropWritable)
		p = appendUleb128(p, streamTypeIdx)
		c.emitCanon(p)
		return c.coreFunc - 1

	case "stream.drop-readable":
		var p []byte
		p = append(p, canonStreamDropReadable)
		p = appendUleb128(p, streamTypeIdx)
		c.emitCanon(p)
		return c.coreFunc - 1

	case "stream.write":
		// Needs memory+async — indirect
		shimFuncIdx := c.aliasShimFunc(*indirectIdx)
		capturedTypeIdx := streamTypeIdx
		c.indirects[*indirectIdx].emitFn = func() {
			var p []byte
			p = append(p, canonStreamWrite)
			p = appendUleb128(p, capturedTypeIdx)
			p = appendUleb128(p, 2)
			p = append(p, optMemory)
			p = appendUleb128(p, c.memoryIdx)
			p = append(p, optAsync)
			c.emitCanon(p)
		}
		*indirectIdx++
		return shimFuncIdx

	case "stream.read":
		shimFuncIdx := c.aliasShimFunc(*indirectIdx)
		capturedTypeIdx := streamTypeIdx
		c.indirects[*indirectIdx].emitFn = func() {
			var p []byte
			p = append(p, canonStreamRead)
			p = appendUleb128(p, capturedTypeIdx)
			p = appendUleb128(p, 2)
			p = append(p, optMemory)
			p = appendUleb128(p, c.memoryIdx)
			p = append(p, optAsync)
			c.emitCanon(p)
		}
		*indirectIdx++
		return shimFuncIdx

	case "future.read":
		// Need the right future type — depends on the interface
		futureTypeIdx := c.getOrCreateFutureType(ifaceName, compInstIdx)
		if isAsyncLower || true { // future.read always needs memory+async
			shimFuncIdx := c.aliasShimFunc(*indirectIdx)
			capturedTypeIdx := futureTypeIdx
			c.indirects[*indirectIdx].emitFn = func() {
				var p []byte
				p = append(p, canonFutureRead)
				p = appendUleb128(p, capturedTypeIdx)
				p = appendUleb128(p, 2)
				p = append(p, optMemory)
				p = appendUleb128(p, c.memoryIdx)
				p = append(p, optAsync)
				c.emitCanon(p)
			}
			*indirectIdx++
			return shimFuncIdx
		}

	case "future.drop-readable":
		futureTypeIdx := c.getOrCreateFutureType(ifaceName, compInstIdx)
		var p []byte
		p = append(p, canonFutureDropReadable)
		p = appendUleb128(p, futureTypeIdx)
		c.emitCanon(p)
		return c.coreFunc - 1
	}

	return 0 // shouldn't happen
}

// streamU8TypeIdx caches the component type index for stream<u8>
var streamU8Sentinel uint32 = 0xFFFFFFFF

func (c *compBuilder) getOrCreateStreamU8Type() uint32 {
	if streamU8Sentinel != 0xFFFFFFFF {
		return streamU8Sentinel
	}
	// Emit type section: stream<u8>
	var typeSec []byte
	typeSec = appendUleb128(typeSec, 1)
	typeSec = append(typeSec, cvtStream, 0x01, cvtU8)
	c.emitSection(secComponentType, typeSec)
	streamU8Sentinel = c.compType
	c.compType++
	return streamU8Sentinel
}

// futureTypes caches per-interface future types
var futureTypeCache = make(map[string]uint32)

func (c *compBuilder) getOrCreateFutureType(ifaceName string, compInstIdx uint32) uint32 {
	if idx, ok := futureTypeCache[ifaceName]; ok {
		return idx
	}

	// For stdout/stderr: future<result<_, error-code>>
	// We need to: alias error-code from the interface, build result type, build future type
	errorCodeIdx := c.aliasCompExport(compInstIdx, "error-code", 0x03)

	// result<_, error-code>
	var typeSec1 []byte
	typeSec1 = appendUleb128(typeSec1, 1)
	typeSec1 = append(typeSec1, cvtResult, 0x00, 0x01)
	typeSec1 = appendUleb128(typeSec1, errorCodeIdx)
	c.emitSection(secComponentType, typeSec1)
	resultTypeIdx := c.compType
	c.compType++

	// future<result<_, error-code>>
	var typeSec2 []byte
	typeSec2 = appendUleb128(typeSec2, 1)
	typeSec2 = append(typeSec2, cvtFuture, 0x01)
	typeSec2 = appendUleb128(typeSec2, resultTypeIdx)
	c.emitSection(secComponentType, typeSec2)
	futureIdx := c.compType
	c.compType++

	futureTypeCache[ifaceName] = futureIdx
	return futureIdx
}

func (c *compBuilder) aliasShimFunc(shimIdx int) uint32 {
	name := fmt.Sprintf("%d", shimIdx)
	c.aliasCoreFuncExport(c.shimInstance, name, []byte{0x00, 0x00})
	return c.coreFunc - 1
}

func (c *compBuilder) needsMemory(imp Import) bool {
	return needsIndirect(imp)
}

func (c *compBuilder) needsRealloc(imp Import) bool {
	// Functions returning strings/lists need realloc
	name := imp.Name
	mod := imp.Module
	if strings.Contains(mod, "cli/environment") {
		return true
	}
	if strings.Contains(mod, "filesystem/preopens") && name == "get-directories" {
		return true
	}
	if strings.Contains(mod, "exec/exec") {
		return true
	}
	return false
}

type coreExport struct {
	name string
	idx  uint32
}

func (c *compBuilder) emitCoreInstanceFromExports(exports []coreExport) {
	var payload []byte
	payload = appendUleb128(payload, 1)
	payload = append(payload, 0x01) // from exports
	payload = appendUleb128(payload, uint32(len(exports)))
	for _, e := range exports {
		payload = appendName(payload, e.name)
		payload = append(payload, 0x00) // sort: core func
		payload = appendUleb128(payload, e.idx)
	}
	c.emitSection(secCoreInstance, payload)
	c.coreInstance++
}

func (c *compBuilder) emitMainInstantiate(mainModIdx uint32) {
	groups := c.groupImports()

	// Build args: map module name -> core instance index
	// The core instances were created in the same order as groups
	var args []instArg
	coreInstBase := c.shimInstance + 1 // first core instance after shim
	for i, g := range groups {
		args = append(args, instArg{
			name:        g.module,
			instanceIdx: coreInstBase + uint32(i),
		})
	}

	c.emitCoreInstantiate(mainModIdx, args)
}

func (c *compBuilder) emitFixupInstantiation(fixupModIdx uint32) {
	if len(c.indirects) == 0 {
		return
	}

	// Create a core instance with: $imports table + all the real lowered funcs
	var exports []coreExport
	exports = append(exports, coreExport{"$imports", 0}) // special: table

	for i := range c.indirects {
		exports = append(exports, coreExport{
			name: fmt.Sprintf("%d", i),
			idx:  0, // will be filled from the post-instantiation canon funcs
		})
	}

	// The fixup args instance needs the table and the real functions
	// The real functions were emitted during post-instantiation
	// They are the last N core funcs before this point
	// Actually, the table is at shimTableIdx
	// The real funcs are at (reallocIdx+1) through (reallocIdx+N)

	var payload []byte
	payload = appendUleb128(payload, 1)
	payload = append(payload, 0x01) // from exports
	numExports := 1 + len(c.indirects)
	payload = appendUleb128(payload, uint32(numExports))

	// Table
	payload = appendName(payload, "$imports")
	payload = append(payload, 0x01) // sort: table
	payload = appendUleb128(payload, c.shimTableIdx)

	// Each real lowered function
	realFuncStart := c.reallocIdx + 1
	for i := range c.indirects {
		payload = appendName(payload, fmt.Sprintf("%d", i))
		payload = append(payload, 0x00) // sort: func
		payload = appendUleb128(payload, realFuncStart+uint32(i))
	}

	c.emitSection(secCoreInstance, payload)
	fixupArgsInstance := c.coreInstance
	c.coreInstance++

	// Instantiate fixup module
	c.emitCoreInstantiate(fixupModIdx, []instArg{{"", fixupArgsInstance}})
}

func (c *compBuilder) emitRunExport() {
	// 1. Alias the [async-lift] and [callback] exports from main instance
	var asyncLiftName, callbackName string
	for _, exp := range c.mod.Exports {
		if strings.HasPrefix(exp.Name, "[async-lift]") {
			asyncLiftName = exp.Name
		}
		if strings.HasPrefix(exp.Name, "[callback][async-lift]") {
			callbackName = exp.Name
		}
	}

	c.aliasCoreFuncExport(c.mainInstance, asyncLiftName, []byte{0x00, 0x00})
	asyncLiftIdx := c.coreFunc - 1
	c.aliasCoreFuncExport(c.mainInstance, callbackName, []byte{0x00, 0x00})
	callbackIdx := c.coreFunc - 1

	// 2. Emit result<> type for the run function
	resultTypeIdx := c.emitResultType()

	// 3. Emit async func type: async func() -> result<>
	var funcTypeSec []byte
	funcTypeSec = appendUleb128(funcTypeSec, 1)
	funcTypeSec = append(funcTypeSec, ttFuncAsync)
	funcTypeSec = appendUleb128(funcTypeSec, 0) // 0 params
	funcTypeSec = append(funcTypeSec, 0x00)     // has result
	funcTypeSec = appendUleb128(funcTypeSec, resultTypeIdx)
	c.emitSection(secComponentType, funcTypeSec)
	runFuncTypeIdx := c.compType
	c.compType++

	// 4. canon lift
	var liftPayload []byte
	liftPayload = append(liftPayload, canonLift, 0x00)
	liftPayload = appendUleb128(liftPayload, asyncLiftIdx)
	liftPayload = appendUleb128(liftPayload, 2) // 2 options
	liftPayload = append(liftPayload, optAsync)
	liftPayload = append(liftPayload, optCallback)
	liftPayload = appendUleb128(liftPayload, callbackIdx)
	liftPayload = appendUleb128(liftPayload, runFuncTypeIdx)
	c.emitCanonLift(liftPayload)
	runFuncIdx := c.compFunc - 1

	// 5. Shim component for re-typing
	var shimComp []byte
	shimComp = append(shimComp, 0x00, 0x61, 0x73, 0x6d, 0x0d, 0x00, 0x01, 0x00)

	// Type: result<>
	var stResult []byte
	stResult = appendUleb128(stResult, 1)
	stResult = append(stResult, cvtResult, 0x00, 0x00)
	shimComp = appendSection(shimComp, secComponentType, stResult)

	// Type: async func() -> result<>
	var stFunc []byte
	stFunc = appendUleb128(stFunc, 1)
	stFunc = append(stFunc, ttFuncAsync)
	stFunc = appendUleb128(stFunc, 0) // 0 params
	stFunc = append(stFunc, 0x00)     // has result
	stFunc = appendUleb128(stFunc, 0) // result: type 0
	shimComp = appendSection(shimComp, secComponentType, stFunc)

	// Import the function
	var stImport []byte
	stImport = appendUleb128(stImport, 1)
	stImport = append(stImport, 0x00) // name kind
	stImport = appendName(stImport, "import-func-run")
	stImport = append(stImport, 0x01)     // extern: func
	stImport = appendUleb128(stImport, 1) // type 1
	shimComp = appendSection(shimComp, secImport, stImport)

	// Re-export with new type
	var stResult2 []byte
	stResult2 = appendUleb128(stResult2, 1)
	stResult2 = append(stResult2, cvtResult, 0x00, 0x00)
	shimComp = appendSection(shimComp, secComponentType, stResult2)

	var stFunc2 []byte
	stFunc2 = appendUleb128(stFunc2, 1)
	stFunc2 = append(stFunc2, ttFuncAsync)
	stFunc2 = appendUleb128(stFunc2, 0) // 0 params
	stFunc2 = append(stFunc2, 0x00)     // has result
	stFunc2 = appendUleb128(stFunc2, 2) // result: type 2
	shimComp = appendSection(shimComp, secComponentType, stFunc2)

	// Export
	var stExport []byte
	stExport = appendUleb128(stExport, 1)
	stExport = append(stExport, 0x00) // name kind
	stExport = appendName(stExport, "run")
	stExport = append(stExport, 0x01)     // extern: func
	stExport = appendUleb128(stExport, 0) // func index 0
	stExport = append(stExport, 0x01)     // has type ascription
	stExport = append(stExport, 0x01)     // extern: func
	stExport = appendUleb128(stExport, 3) // type 3
	shimComp = appendSection(shimComp, secExport, stExport)

	// Emit the shim component
	c.out = append(c.out, secComponent)
	c.out = appendUleb128(c.out, uint32(len(shimComp)))
	c.out = append(c.out, shimComp...)
	shimCompIdx := c.compComponent
	c.compComponent++

	// 6. Instantiate shim component
	var compInstPayload []byte
	compInstPayload = appendUleb128(compInstPayload, 1)
	compInstPayload = append(compInstPayload, 0x00) // instantiate
	compInstPayload = appendUleb128(compInstPayload, shimCompIdx)
	compInstPayload = appendUleb128(compInstPayload, 1) // 1 arg
	compInstPayload = appendName(compInstPayload, "import-func-run")
	compInstPayload = append(compInstPayload, 0x01) // sort: func
	compInstPayload = appendUleb128(compInstPayload, runFuncIdx)
	c.emitSection(secCompInstance, compInstPayload)
	shimInstIdx := c.compInstance
	c.compInstance++

	// 7. Export the instance as wasi:cli/run@...
	var exportPayload []byte
	exportPayload = appendUleb128(exportPayload, 1)
	exportPayload = append(exportPayload, 0x00) // name kind
	exportPayload = appendName(exportPayload, "wasi:cli/run@"+wasiVersion)
	exportPayload = append(exportPayload, 0x05) // extern: instance
	exportPayload = appendUleb128(exportPayload, shimInstIdx)
	exportPayload = append(exportPayload, 0x00) // no type ascription
	c.emitSection(secExport, exportPayload)
}

// --- Helpers ---

func appendSection(b []byte, id byte, payload []byte) []byte {
	b = append(b, id)
	b = appendUleb128(b, uint32(len(payload)))
	b = append(b, payload...)
	return b
}

func appendCustomSection(module []byte, name string, data []byte) []byte {
	var payload []byte
	payload = appendName(payload, name)
	payload = append(payload, data...)

	result := make([]byte, 0, len(module)+1+5+len(payload))
	result = append(result, module...)
	result = appendSection(result, secCustom, payload)
	return result
}

func buildCustomSectionPayload(name string, data []byte) []byte {
	var payload []byte
	payload = appendName(payload, name)
	payload = append(payload, data...)
	return payload
}

func buildProducersPayload() []byte {
	var b []byte
	b = appendUleb128(b, 1)           // 1 field
	b = appendName(b, "processed-by") // field name
	b = appendUleb128(b, 1)           // 1 value
	b = appendName(b, "go-componentize")
	b = appendName(b, "0.1.0")
	return b
}

func stripCustomSection(data []byte, name string) ([]byte, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("module too short")
	}
	result := make([]byte, 0, len(data))
	result = append(result, data[:8]...)
	offset := 8
	for offset < len(data) {
		sectionStart := offset
		sectionID := data[offset]
		offset++
		sectionLen, newOffset, err := readUleb128(data, offset)
		if err != nil {
			return nil, err
		}
		offset = newOffset
		sectionEnd := offset + int(sectionLen)
		if sectionEnd > len(data) {
			return nil, fmt.Errorf("section extends beyond data")
		}
		if sectionID == 0 {
			sName, _, err := readName(data, offset)
			if err != nil {
				return nil, err
			}
			if sName == name {
				offset = sectionEnd
				continue
			}
		}
		result = append(result, data[sectionStart:sectionEnd]...)
		offset = sectionEnd
	}
	return result, nil
}

func encodeFuncTypeKey(ft FuncType) string {
	var b []byte
	b = append(b, ft.Params...)
	b = append(b, '|')
	b = append(b, ft.Results...)
	return string(b)
}

func appendSleb128(b []byte, v int32) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && c&0x40 == 0) || (v == -1 && c&0x40 != 0) {
			b = append(b, c)
			break
		}
		b = append(b, c|0x80)
	}
	return b
}

func parseContextIdx(name string) uint32 {
	// Parse "[context-get-N]" or "[context-set-N]"
	start := strings.LastIndex(name, "-")
	end := strings.Index(name, "]")
	if start >= 0 && end > start {
		s := name[start+1 : end]
		var n uint32
		for _, c := range s {
			n = n*10 + uint32(c-'0')
		}
		return n
	}
	return 0
}
