// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package componentize

// This file translates parsed WIT interfaces into the component binary
// type/import/alias preamble sections. It reads WIT files from
// src/internal/wasi/wit/ and generates the component type encoding.

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// ifaceImport describes a component-level interface import.
type ifaceImport struct {
	name  string         // e.g. "wasi:cli/environment@0.3.0-rc-2026-02-09"
	items []instanceItem // the instance type definition items

	// After import, which type exports to alias into the outer scope.
	aliases []aliasExport
}

type aliasExport struct {
	exportName string
}

// witResolver holds all parsed WIT packages and resolves cross-interface references.
type witResolver struct {
	packages   []*WitPackage
	interfaces map[string]*WitInterface // fully qualified name -> interface
}

func newWitResolver(packages []*WitPackage) *witResolver {
	r := &witResolver{
		packages:   packages,
		interfaces: make(map[string]*WitInterface),
	}
	for _, pkg := range packages {
		for _, iface := range pkg.Interfaces {
			key := fmt.Sprintf("%s:%s/%s@%s", pkg.Namespace, pkg.Name, iface.Name, pkg.Version)
			r.interfaces[key] = iface
		}
	}
	return r
}

// resolveInterface looks up an interface by its qualified name.
func (r *witResolver) resolveInterface(name string) *WitInterface {
	return r.interfaces[name]
}

// resolveLocalRef resolves a local interface name within a package to its qualified name.
func (r *witResolver) resolveLocalRef(pkg *WitPackage, localName string) string {
	return fmt.Sprintf("%s:%s/%s@%s", pkg.Namespace, pkg.Name, localName, pkg.Version)
}

// instanceBuilder converts a WIT interface into instance items.
type instanceBuilder struct {
	items   []instanceItem
	typeIdx uint32 // tracks the next type index within this instance

	// Maps type names to their local index within the instance.
	typeMap map[string]uint32

	// Tracks which type names are resources (need own wrapper in value positions).
	resources map[string]bool

	// Outer aliases needed (from types imported via `use`)
	outerAliases map[string]uint32 // "qualified-iface:type-name" -> outer type idx
}

func newInstanceBuilder() *instanceBuilder {
	return &instanceBuilder{
		typeMap:      make(map[string]uint32),
		resources:    make(map[string]bool),
		outerAliases: make(map[string]uint32),
	}
}

func (b *instanceBuilder) addType(name string, typ vtype) uint32 {
	b.items = append(b.items, instTypeDef{typ})
	idx := b.typeIdx
	b.typeMap[name] = idx
	b.typeIdx++
	return idx
}

// addAnonymousType adds an unnamed type def and returns its index.
func (b *instanceBuilder) addAnonymousType(typ vtype) uint32 {
	b.items = append(b.items, instTypeDef{typ})
	idx := b.typeIdx
	b.typeIdx++
	return idx
}

// toValtype ensures a vtype is a valid valtype (primitive or type reference).
// Complex types (list, tuple, record, etc.) are emitted as anonymous type defs
// and referenced by index, since the component binary format only allows
// primitives and type indices in function param/result positions.
func (b *instanceBuilder) toValtype(v vtype) vtype {
	switch v.(type) {
	case primType, typeRef:
		return v
	default:
		idx := b.addAnonymousType(v)
		return typeRef(idx)
	}
}

// addResource adds a resource to the instance as an export with (sub resource)
// typebound. In import contexts, resources cannot be defined inline — they must
// be declared via export with sub resource typebound.
func (b *instanceBuilder) addResource(name string) uint32 {
	b.items = append(b.items, instResourceExport{name})
	idx := b.typeIdx
	b.typeMap[name] = idx
	b.resources[name] = true
	b.typeIdx++
	return idx
}

func (b *instanceBuilder) addFunc(sig funcSig) uint32 {
	b.items = append(b.items, instFuncDef{sig})
	idx := b.typeIdx
	b.typeIdx++
	return idx
}

func (b *instanceBuilder) addExport(name string, sort byte, idx uint32) {
	b.items = append(b.items, instExport{name, sort, idx})
	// Type exports (sort 0x03) introduce a new type index that aliases the
	// exported type. Update typeMap so subsequent references use the export
	// index (required for import validation).
	if sort == 0x03 {
		exportIdx := b.typeIdx
		b.typeIdx++
		b.typeMap[name] = exportIdx
	}
}

func (b *instanceBuilder) addAlias(outerCount, outerIdx uint32) uint32 {
	b.items = append(b.items, instAlias{outerCount, outerIdx})
	idx := b.typeIdx
	b.typeIdx++
	return idx
}

// witTypeToVtype converts a WIT type reference to a defvaltype for encoding.
// Complex types (list, tuple, etc.) are returned as composite vtypes that
// contain only valid valtype references (primitives or type indices) inside.
// The returned vtype itself may be a defvaltype (not a valid valtype);
// callers that need a valtype should use toValtype() on the result.
func (b *instanceBuilder) witTypeToVtype(ref WitTypeRef) vtype {
	switch r := ref.(type) {
	case WitPrimitive:
		return primToVtype(r.Name)
	case WitNamedRef:
		idx, ok := b.typeMap[r.Name]
		if !ok {
			panic(fmt.Sprintf("unresolved type reference: %s", r.Name))
		}
		// Resource types used as values are implicitly own<T> in WIT
		if b.resources[r.Name] {
			return ownType{typeRef(idx)}
		}
		return typeRef(idx)
	case WitListRef:
		return listType{b.toValtype(b.witTypeToVtype(r.Elem))}
	case WitOptionRef:
		return optionType{b.toValtype(b.witTypeToVtype(r.Inner))}
	case WitResultRef:
		var ok, err vtype
		if r.Ok != nil {
			ok = b.toValtype(b.witTypeToVtype(r.Ok))
		}
		if r.Err != nil {
			err = b.toValtype(b.witTypeToVtype(r.Err))
		}
		return resultType{ok, err}
	case WitTupleRef:
		fields := make([]vtype, len(r.Elems))
		for i, e := range r.Elems {
			fields[i] = b.toValtype(b.witTypeToVtype(e))
		}
		return tupleType{fields}
	case WitStreamRef:
		var elem vtype
		if r.Elem != nil {
			elem = b.toValtype(b.witTypeToVtype(r.Elem))
		}
		return streamType{elem}
	case WitFutureRef:
		var elem vtype
		if r.Elem != nil {
			elem = b.toValtype(b.witTypeToVtype(r.Elem))
		}
		return futureType{elem}
	case WitBorrowRef:
		idx, ok := b.typeMap[r.Resource]
		if !ok {
			panic(fmt.Sprintf("unresolved resource for borrow: %s", r.Resource))
		}
		return borrowType{typeRef(idx)}
	case WitOwnRef:
		idx, ok := b.typeMap[r.Resource]
		if !ok {
			panic(fmt.Sprintf("unresolved resource for own: %s", r.Resource))
		}
		return ownType{typeRef(idx)}
	default:
		panic(fmt.Sprintf("unknown type ref: %T", ref))
	}
}

func primToVtype(name string) vtype {
	switch name {
	case "bool":
		return vtBool
	case "u8":
		return vtU8
	case "u16":
		return vtU16
	case "u32":
		return vtU32
	case "u64":
		return vtU64
	case "s8":
		return primType(cvtS8)
	case "s16":
		return primType(cvtS16)
	case "s32":
		return vtS32
	case "s64":
		return vtS64
	case "f32":
		return vtF32
	case "f64":
		return vtF64
	case "char":
		return vtChar
	case "string":
		return vtString
	default:
		panic(fmt.Sprintf("unknown primitive: %s", name))
	}
}

// buildInterfaceImport creates an ifaceImport from a WIT interface.
// outerTypes maps "qualified-name:export-name" -> outer type index for use aliases.
// outerResources tracks which outer types are resources (need own wrapper).
func buildInterfaceImport(qualifiedName string, iface *WitInterface, outerTypes map[string]uint32, outerResources map[string]bool) ifaceImport {
	b := newInstanceBuilder()

	// Extract the package prefix from the qualified name for resolving local uses
	// e.g. "wasi:cli/stdout@0.3.0-rc-2026-02-09" -> "wasi:cli"
	// Then a local use "types" resolves to "wasi:cli/types@0.3.0-rc-2026-02-09"
	pkgPrefix, version := extractPkgPrefix(qualifiedName)

	// 1. Handle `use` statements — create outer aliases for imported types
	for _, u := range iface.Uses {
		for localName, remoteName := range u.Names {
			// Find the outer type index for this used type
			key := u.From + ":" + remoteName
			outerIdx, ok := outerTypes[key]
			if !ok {
				// Try resolving as local reference within same package
				if !strings.Contains(u.From, ":") && pkgPrefix != "" {
					localKey := pkgPrefix + "/" + u.From
					if version != "" {
						localKey += "@" + version
					}
					localKey += ":" + remoteName
					outerIdx, ok = outerTypes[localKey]
				}
			}
			if !ok {
				continue
			}
			aliasIdx := b.addAlias(1, outerIdx)
			b.typeMap[localName] = aliasIdx
			// Check if this is a resource type
			if outerResources[key] {
				b.resources[localName] = true
			} else if !strings.Contains(u.From, ":") && pkgPrefix != "" {
				localKey := pkgPrefix + "/" + u.From
				if version != "" {
					localKey += "@" + version
				}
				localKey += ":" + remoteName
				if outerResources[localKey] {
					b.resources[localName] = true
				}
			}
			// Export the aliased type
			b.addExport(localName, 0x03, aliasIdx)
		}
	}

	// 2. Process type definitions
	var exportedTypes []string
	for _, td := range iface.TypeDefs {
		switch k := td.Kind.(type) {
		case WitAlias:
			idx := b.addType(td.Name, b.witTypeToVtype(k.Type))
			b.addExport(td.Name, 0x03, idx)
			exportedTypes = append(exportedTypes, td.Name)
		case WitRecord:
			fields := make([]recordField, len(k.Fields))
			for i, f := range k.Fields {
				fields[i] = recordField{f.Name, b.toValtype(b.witTypeToVtype(f.Type))}
			}
			idx := b.addType(td.Name, recordType{fields})
			b.addExport(td.Name, 0x03, idx)
			exportedTypes = append(exportedTypes, td.Name)
		case WitVariant:
			cases := make([]variantCase, len(k.Cases))
			for i, c := range k.Cases {
				var typ vtype
				if c.Type != nil {
					typ = b.toValtype(b.witTypeToVtype(c.Type))
				}
				cases[i] = variantCase{c.Name, typ}
			}
			idx := b.addType(td.Name, variantType{cases})
			b.addExport(td.Name, 0x03, idx)
			exportedTypes = append(exportedTypes, td.Name)
		case WitEnum:
			idx := b.addType(td.Name, enumType{k.Cases})
			b.addExport(td.Name, 0x03, idx)
			exportedTypes = append(exportedTypes, td.Name)
		case WitFlags:
			idx := b.addType(td.Name, flagsType{k.Flags})
			b.addExport(td.Name, 0x03, idx)
			exportedTypes = append(exportedTypes, td.Name)
		case WitResource:
			b.addResource(td.Name) // creates export with (sub resource) typebound
			exportedTypes = append(exportedTypes, td.Name)
			// Add resource methods
			for _, m := range k.Methods {
				sig := buildMethodSig(b, m)
				funcIdx := b.addFunc(sig)
				exportName := methodExportName(td.Name, m)
				b.addExport(exportName, 0x01, funcIdx)
			}
		}
	}

	// 3. Process freestanding functions
	for _, f := range iface.Functions {
		sig := buildFuncSig(b, f)
		funcIdx := b.addFunc(sig)
		b.addExport(f.Name, 0x01, funcIdx)
	}

	// 4. Determine which types need to be aliased into outer scope
	// Types that are referenced by other interfaces via `use` need aliases.
	var aliases []aliasExport
	for _, name := range exportedTypes {
		aliases = append(aliases, aliasExport{name})
	}

	return ifaceImport{
		name:    qualifiedName,
		items:   b.items,
		aliases: aliases,
	}
}

func buildFuncSig(b *instanceBuilder, f *WitFunc) funcSig {
	params := make([]funcParam, len(f.Params))
	for i, p := range f.Params {
		params[i] = funcParam{p.Name, b.toValtype(b.witTypeToVtype(p.Type))}
	}
	var result vtype
	if f.Result != nil {
		result = b.toValtype(b.witTypeToVtype(f.Result))
	}
	return funcSig{async: f.IsAsync, params: params, result: result}
}

func buildMethodSig(b *instanceBuilder, m *WitFunc) funcSig {
	var params []funcParam
	if m.Kind == "method" {
		// Add implicit self parameter
		resIdx := b.typeMap[m.ResourceName]
		// Create borrow type for self if not already present
		borrowKey := "__borrow_" + m.ResourceName
		if _, ok := b.typeMap[borrowKey]; !ok {
			idx := b.addType(borrowKey, borrowType{typeRef(resIdx)})
			b.typeMap[borrowKey] = idx
		}
		borrowIdx := b.typeMap[borrowKey]
		params = append(params, funcParam{"self", typeRef(borrowIdx)})
	}
	for _, p := range m.Params {
		params = append(params, funcParam{p.Name, b.toValtype(b.witTypeToVtype(p.Type))})
	}
	var result vtype
	if m.Result != nil {
		result = b.toValtype(b.witTypeToVtype(m.Result))
	}
	return funcSig{async: m.IsAsync, params: params, result: result}
}

func methodExportName(resourceName string, m *WitFunc) string {
	switch m.Kind {
	case "method":
		return "[method]" + resourceName + "." + m.Name
	case "static":
		return "[static]" + resourceName + "." + m.Name
	case "constructor":
		return "[constructor]" + resourceName
	default:
		return m.Name
	}
}

// preambleBuilder generates the type+import+alias sections for the world preamble.
type preambleBuilder struct {
	out         []byte
	typeIdx     uint32
	instanceIdx uint32

	// Map of interface name -> instance index (for aliases)
	instanceMap map[string]uint32
	// Map of "interface:export" -> outer type index (for cross-interface refs)
	aliasedTypes map[string]uint32
}

func (p *preambleBuilder) init() {
	p.instanceMap = make(map[string]uint32)
	p.aliasedTypes = make(map[string]uint32)
}

func (p *preambleBuilder) emitSection(id byte, payload []byte) {
	p.out = append(p.out, id)
	p.out = appendUleb128(p.out, uint32(len(payload)))
	p.out = append(p.out, payload...)
}

func (p *preambleBuilder) emitTypeSection(instanceTypeBytes []byte) {
	var payload []byte
	payload = appendUleb128(payload, 1)
	payload = append(payload, instanceTypeBytes...)
	p.emitSection(0x07, payload)
	p.typeIdx++
}

func (p *preambleBuilder) emitImportSection(name string, typeIdx uint32) {
	var payload []byte
	payload = appendUleb128(payload, 1)
	payload = append(payload, 0x00) // name kind: kebab
	payload = appendName(payload, name)
	payload = append(payload, 0x05) // extern: instance
	payload = appendUleb128(payload, typeIdx)
	p.emitSection(0x0a, payload)
	p.instanceIdx++
}

func (p *preambleBuilder) emitAliasSection(instanceIdx uint32, exportName string) uint32 {
	var payload []byte
	payload = appendUleb128(payload, 1)
	payload = append(payload, 0x03) // sort: type
	payload = append(payload, 0x00) // kind: instance export
	payload = appendUleb128(payload, instanceIdx)
	payload = appendName(payload, exportName)
	p.emitSection(0x06, payload)
	idx := p.typeIdx
	p.typeIdx++
	return idx
}

// emitInterface emits the type, import, and alias sections for one interface.
func (p *preambleBuilder) emitInterface(iface ifaceImport) {
	if len(iface.items) > 0 {
		typeBytes := encodeInstanceType(iface.items)
		typeIdx := p.typeIdx
		p.emitTypeSection(typeBytes)
		p.emitImportSection(iface.name, typeIdx)
	}

	instIdx := p.instanceIdx - 1
	p.instanceMap[iface.name] = instIdx

	for _, alias := range iface.aliases {
		aliasIdx := p.emitAliasSection(instIdx, alias.exportName)
		p.aliasedTypes[iface.name+":"+alias.exportName] = aliasIdx
	}
}

// buildWorldFromWIT reads WIT files and builds the preamble.
// witDir should point to the directory containing worlds.wit and deps/.
func buildWorldFromWIT(witDir string, worldName string) ([]ifaceImport, *WitWorld, []*WitPackage, error) {
	packages, err := ParseWitDirectory(witDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parsing WIT directory: %w", err)
	}

	resolver := newWitResolver(packages)

	// Find the target world
	var world *WitWorld
	var worldPkg *WitPackage
	for _, pkg := range packages {
		for _, w := range pkg.Worlds {
			if w.Name == worldName {
				world = w
				worldPkg = pkg
				break
			}
		}
	}
	if world == nil {
		return nil, nil, nil, fmt.Errorf("world %q not found in WIT files", worldName)
	}

	// Resolve the ordered list of interfaces, including transitive dependencies.
	// For each interface in the world's imports, check if it has `use` statements
	// that reference other interfaces not yet in the list.
	var orderedNames []string
	added := make(map[string]bool)

	var addWithDeps func(name string)
	addWithDeps = func(name string) {
		if added[name] {
			return
		}
		iface := resolver.resolveInterface(name)
		if iface == nil {
			return
		}
		// First add dependencies
		for _, u := range iface.Uses {
			depName := resolveUsePath(u, name)
			addWithDeps(depName)
		}
		added[name] = true
		orderedNames = append(orderedNames, name)
	}

	for _, imp := range world.Imports {
		qualifiedName := resolveWorldItemName(imp, worldPkg, resolver)
		addWithDeps(qualifiedName)
	}

	// Build ifaceImport structs in order
	var ifaces []ifaceImport
	outerTypes := make(map[string]uint32)
	outerResources := make(map[string]bool) // tracks which outer types are resources

	pb := &preambleBuilder{}
	pb.init()

	for _, qualifiedName := range orderedNames {
		iface := resolver.resolveInterface(qualifiedName)
		if iface == nil {
			continue
		}

		ifImp := buildInterfaceImport(qualifiedName, iface, outerTypes, outerResources)
		ifImp.aliases = filterUsedAliases(ifImp, iface, world, resolver, worldPkg)

		ifaces = append(ifaces, ifImp)

		// Simulate preamble emission to track outer type indices
		if len(ifImp.items) > 0 {
			pb.typeIdx++
			pb.instanceIdx++
		}
		instIdx := pb.instanceIdx - 1
		pb.instanceMap[qualifiedName] = instIdx

		for _, alias := range ifImp.aliases {
			aliasIdx := pb.typeIdx
			pb.typeIdx++
			key := qualifiedName + ":" + alias.exportName
			outerTypes[key] = aliasIdx
			// Check if this type is a resource in the source interface
			for _, td := range iface.TypeDefs {
				if td.Name == alias.exportName {
					if _, isResource := td.Kind.(WitResource); isResource {
						outerResources[key] = true
					}
					break
				}
			}
		}
	}

	return ifaces, world, packages, nil
}

// resolveWorldItemName resolves a world import/export name to a fully qualified interface name.
func resolveWorldItemName(item WitWorldItem, pkg *WitPackage, resolver *witResolver) string {
	name := item.InterfaceName
	if strings.Contains(name, ":") {
		// Already qualified
		return name
	}
	// Local reference within the same package
	return resolver.resolveLocalRef(pkg, name)
}

// filterUsedAliases determines which exported types from an interface need to be
// aliased into the outer scope because they're referenced by later interfaces.
func filterUsedAliases(ifImp ifaceImport, iface *WitInterface, world *WitWorld, resolver *witResolver, worldPkg *WitPackage) []aliasExport {
	// Collect all type names that other interfaces use from this one
	qualifiedName := ifImp.name
	usedNames := make(map[string]bool)

	// Check all subsequent world imports for `use` statements referencing this interface
	for _, imp := range world.Imports {
		impName := resolveWorldItemName(imp, worldPkg, resolver)
		otherIface := resolver.resolveInterface(impName)
		if otherIface == nil {
			continue
		}
		for _, u := range otherIface.Uses {
			usePath := resolveUsePath(u, impName)
			if usePath == qualifiedName {
				for _, remoteName := range u.Names {
					usedNames[remoteName] = true
				}
			}
		}
	}

	// Also check world exports
	for _, exp := range world.Exports {
		expName := resolveWorldItemName(exp, worldPkg, resolver)
		otherIface := resolver.resolveInterface(expName)
		if otherIface == nil {
			continue
		}
		for _, u := range otherIface.Uses {
			usePath := resolveUsePath(u, expName)
			if usePath == qualifiedName {
				for _, remoteName := range u.Names {
					usedNames[remoteName] = true
				}
			}
		}
	}

	// Also: resources that are used via borrow/own in other interfaces need aliases
	// For filesystem/types, "descriptor" is used by preopens
	// For now, include all resource types as potential aliases
	for _, td := range iface.TypeDefs {
		if _, ok := td.Kind.(WitResource); ok {
			usedNames[td.Name] = true
		}
	}

	var aliases []aliasExport
	for name := range usedNames {
		aliases = append(aliases, aliasExport{name})
	}
	// Sort for determinism
	sort.Slice(aliases, func(i, j int) bool {
		return aliases[i].exportName < aliases[j].exportName
	})
	return aliases
}

// resolveUsePath resolves a `use` statement's `from` path to a fully qualified interface name.
// ifaceQualifiedName is the qualified name of the interface containing the `use` statement.
func resolveUsePath(u *WitUse, ifaceQualifiedName string) string {
	if strings.Contains(u.From, ":") {
		return u.From
	}
	// Local reference: resolve relative to the containing interface's package
	pkgPrefix, version := extractPkgPrefix(ifaceQualifiedName)
	result := pkgPrefix + "/" + u.From
	if version != "" {
		result += "@" + version
	}
	return result
}

// buildPreamble generates the full preamble for a world.
// It selects the world based on GOEXPERIMENT: "command-with-exec" if
// wasiexec is enabled, "command" otherwise.
func buildPreamble() ([]byte, *preambleBuilder) {
	world := "command"
	if exp := os.Getenv("GOEXPERIMENT"); strings.Contains(exp, "wasiexec") {
		world = "command-with-exec"
	}
	return buildPreambleFromWorld(defaultWitDir(), world)
}

// buildPreambleFromWorld generates the preamble for a specific world from WIT files.
func buildPreambleFromWorld(witDir string, worldName string) ([]byte, *preambleBuilder) {
	ifaces, _, _, err := buildWorldFromWIT(witDir, worldName)
	if err != nil {
		panic(fmt.Sprintf("building world from WIT: %v", err))
	}

	p := &preambleBuilder{}
	p.init()

	for _, ifImp := range ifaces {
		p.emitInterface(ifImp)
	}

	return p.out, p
}

// extractPkgPrefix extracts the package prefix and version from a qualified interface name.
// e.g. "wasi:cli/stdout@0.3.0-rc-2026-02-09" -> ("wasi:cli", "0.3.0-rc-2026-02-09")
func extractPkgPrefix(qualifiedName string) (string, string) {
	// Split off version
	version := ""
	name := qualifiedName
	if idx := strings.Index(name, "@"); idx >= 0 {
		version = name[idx+1:]
		name = name[:idx]
	}
	// Split off interface name
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		return name[:idx], version
	}
	return name, version
}

// defaultWitDir returns the path to the WIT directory.
// It tries GOROOT first, then falls back to relative paths.
func defaultWitDir() string {
	candidates := []string{
		// From test directory (src/internal/wasm/componentize/)
		"../../wasi/wit",
		// From repo root
		"src/internal/wasi/wit",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	// Try GOROOT env
	if goroot := os.Getenv("GOROOT"); goroot != "" {
		dir := goroot + "/src/internal/wasi/wit"
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}
	return candidates[0]
}
