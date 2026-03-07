// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package componentize

import "fmt"

// Core wasm value types.
const (
	valTypeI32 byte = 0x7f
	valTypeI64 byte = 0x7e
	valTypeF32 byte = 0x7d
	valTypeF64 byte = 0x7c
)

// Core wasm section IDs.
const (
	sectionCustom   byte = 0
	sectionType     byte = 1
	sectionImport   byte = 2
	sectionFunction byte = 3
	sectionTable    byte = 4
	sectionMemory   byte = 5
	sectionGlobal   byte = 6
	sectionExport   byte = 7
	sectionStart    byte = 8
	sectionElement  byte = 9
	sectionCode     byte = 10
	sectionData     byte = 11
)

// Import/export kinds.
const (
	importFunc   byte = 0x00
	importTable  byte = 0x01
	importMemory byte = 0x02
	importGlobal byte = 0x03
)

// FuncType represents a core wasm function type.
type FuncType struct {
	Params  []byte // value types
	Results []byte // value types
}

// Import represents a core wasm import.
type Import struct {
	Module  string
	Name    string
	Kind    byte   // importFunc, importTable, etc.
	TypeIdx uint32 // for func imports
}

// Export represents a core wasm export.
type Export struct {
	Name string
	Kind byte   // importFunc, importTable, etc.
	Idx  uint32 // index of the exported item
}

// CoreModule holds the parsed sections of a core wasm module that we need.
type CoreModule struct {
	Types   []FuncType
	Imports []Import
	Exports []Export
}

// ParseCoreModule parses the type, import, and export sections from a core wasm module.
func ParseCoreModule(data []byte) (*CoreModule, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("module too short")
	}
	// Check magic + version
	if data[0] != 0x00 || data[1] != 0x61 || data[2] != 0x73 || data[3] != 0x6d {
		return nil, fmt.Errorf("not a wasm module")
	}
	if data[4] != 0x01 || data[5] != 0x00 || data[6] != 0x00 || data[7] != 0x00 {
		return nil, fmt.Errorf("unsupported wasm version")
	}

	m := &CoreModule{}
	offset := 8

	for offset < len(data) {
		if offset >= len(data) {
			break
		}
		sectionID := data[offset]
		offset++

		sectionLen, newOffset, err := readUleb128(data, offset)
		if err != nil {
			return nil, fmt.Errorf("reading section length: %w", err)
		}
		offset = newOffset
		sectionEnd := offset + int(sectionLen)
		if sectionEnd > len(data) {
			return nil, fmt.Errorf("section %d length %d exceeds data", sectionID, sectionLen)
		}

		switch sectionID {
		case sectionType:
			if err := m.parseTypeSection(data, offset, sectionEnd); err != nil {
				return nil, err
			}
		case sectionImport:
			if err := m.parseImportSection(data, offset, sectionEnd); err != nil {
				return nil, err
			}
		case sectionExport:
			if err := m.parseExportSection(data, offset, sectionEnd); err != nil {
				return nil, err
			}
		}

		offset = sectionEnd
	}

	return m, nil
}

func (m *CoreModule) parseTypeSection(data []byte, offset, end int) error {
	count, offset, err := readUleb128(data, offset)
	if err != nil {
		return fmt.Errorf("type section count: %w", err)
	}

	m.Types = make([]FuncType, count)
	for i := uint32(0); i < count; i++ {
		if offset >= end {
			return fmt.Errorf("unexpected end of type section at type %d", i)
		}
		if data[offset] != 0x60 {
			return fmt.Errorf("expected func type marker 0x60, got 0x%02x at offset %d", data[offset], offset)
		}
		offset++

		// Params
		paramCount, newOffset, err := readUleb128(data, offset)
		if err != nil {
			return fmt.Errorf("type %d param count: %w", i, err)
		}
		offset = newOffset
		params := make([]byte, paramCount)
		for j := uint32(0); j < paramCount; j++ {
			params[j] = data[offset]
			offset++
		}

		// Results
		resultCount, newOffset, err := readUleb128(data, offset)
		if err != nil {
			return fmt.Errorf("type %d result count: %w", i, err)
		}
		offset = newOffset
		results := make([]byte, resultCount)
		for j := uint32(0); j < resultCount; j++ {
			results[j] = data[offset]
			offset++
		}

		m.Types[i] = FuncType{Params: params, Results: results}
	}
	return nil
}

func (m *CoreModule) parseImportSection(data []byte, offset, end int) error {
	count, offset, err := readUleb128(data, offset)
	if err != nil {
		return fmt.Errorf("import section count: %w", err)
	}

	m.Imports = make([]Import, count)
	for i := uint32(0); i < count; i++ {
		modName, newOffset, err := readName(data, offset)
		if err != nil {
			return fmt.Errorf("import %d module name: %w", i, err)
		}
		offset = newOffset

		fieldName, newOffset, err := readName(data, offset)
		if err != nil {
			return fmt.Errorf("import %d field name: %w", i, err)
		}
		offset = newOffset

		kind := data[offset]
		offset++

		imp := Import{Module: modName, Name: fieldName, Kind: kind}

		switch kind {
		case importFunc:
			typeIdx, newOffset, err := readUleb128(data, offset)
			if err != nil {
				return fmt.Errorf("import %d type index: %w", i, err)
			}
			offset = newOffset
			imp.TypeIdx = typeIdx
		case importTable:
			// reftype + limits
			offset++ // reftype
			flags := data[offset]
			offset++
			_, offset, err = readUleb128(data, offset) // min
			if err != nil {
				return err
			}
			if flags&1 != 0 {
				_, offset, err = readUleb128(data, offset) // max
				if err != nil {
					return err
				}
			}
		case importMemory:
			// limits
			flags := data[offset]
			offset++
			_, offset, err = readUleb128(data, offset)
			if err != nil {
				return err
			}
			if flags&1 != 0 {
				_, offset, err = readUleb128(data, offset)
				if err != nil {
					return err
				}
			}
		case importGlobal:
			offset++ // valtype
			offset++ // mut
		default:
			return fmt.Errorf("unknown import kind 0x%02x", kind)
		}

		m.Imports[i] = imp
	}
	return nil
}

func (m *CoreModule) parseExportSection(data []byte, offset, end int) error {
	count, offset, err := readUleb128(data, offset)
	if err != nil {
		return fmt.Errorf("export section count: %w", err)
	}

	m.Exports = make([]Export, count)
	for i := uint32(0); i < count; i++ {
		name, newOffset, err := readName(data, offset)
		if err != nil {
			return fmt.Errorf("export %d name: %w", i, err)
		}
		offset = newOffset

		kind := data[offset]
		offset++

		idx, newOffset, err := readUleb128(data, offset)
		if err != nil {
			return fmt.Errorf("export %d index: %w", i, err)
		}
		offset = newOffset

		m.Exports[i] = Export{Name: name, Kind: kind, Idx: idx}
	}
	return nil
}
