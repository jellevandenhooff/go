// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package componentize

// Component model binary encoding helpers.
// These build the type/import/alias/canon sections that make up a component.

// Component value type tags (from the component model binary format spec).
const (
	cvtBool   byte = 0x7f
	cvtS8     byte = 0x7e
	cvtU8     byte = 0x7d
	cvtS16    byte = 0x7c
	cvtU16    byte = 0x7b
	cvtS32    byte = 0x7a
	cvtU32    byte = 0x79
	cvtS64    byte = 0x78
	cvtU64    byte = 0x77
	cvtF32    byte = 0x76
	cvtF64    byte = 0x75
	cvtChar   byte = 0x74
	cvtString byte = 0x73

	cvtRecord  byte = 0x72
	cvtVariant byte = 0x71
	cvtList    byte = 0x70
	cvtTuple   byte = 0x6f
	cvtFlags   byte = 0x6e
	cvtEnum    byte = 0x6d
	cvtOption  byte = 0x6b
	cvtResult  byte = 0x6a
	cvtOwn     byte = 0x69
	cvtBorrow  byte = 0x68
	cvtStream  byte = 0x66
	cvtFuture  byte = 0x65
	cvtErrCtx  byte = 0x64
)

// Top-level type tags.
const (
	ttFunc          byte = 0x40
	ttComponentType byte = 0x41
	ttInstanceType  byte = 0x42
	ttResource      byte = 0x3f
	ttFuncAsync     byte = 0x43
)

// Instance type item kinds.
const (
	itType       byte = 0x01 // defines a type
	itAlias      byte = 0x02 // alias (outer)
	itExportFunc byte = 0x04 // export a named item
	itExportType byte = 0x04 // same opcode, different sort
)

// vtype represents a component model value type for encoding.
type vtype interface {
	encode(b []byte) []byte
}

// Primitive types.
type primType byte

func (p primType) encode(b []byte) []byte { return append(b, byte(p)) }

// Convenience constants.
var (
	vtBool   vtype = primType(cvtBool)
	vtU8     vtype = primType(cvtU8)
	vtU16    vtype = primType(cvtU16)
	vtU32    vtype = primType(cvtU32)
	vtU64    vtype = primType(cvtU64)
	vtS32    vtype = primType(cvtS32)
	vtS64    vtype = primType(cvtS64)
	vtF32    vtype = primType(cvtF32)
	vtF64    vtype = primType(cvtF64)
	vtChar   vtype = primType(cvtChar)
	vtString vtype = primType(cvtString)
)

// Composite types.

type listType struct{ elem vtype }

func (l listType) encode(b []byte) []byte {
	b = append(b, cvtList)
	return l.elem.encode(b)
}

type tupleType struct{ fields []vtype }

func (t tupleType) encode(b []byte) []byte {
	b = append(b, cvtTuple)
	b = appendUleb128(b, uint32(len(t.fields)))
	for _, f := range t.fields {
		b = f.encode(b)
	}
	return b
}

type recordField struct {
	name string
	typ  vtype
}
type recordType struct{ fields []recordField }

func (r recordType) encode(b []byte) []byte {
	b = append(b, cvtRecord)
	b = appendUleb128(b, uint32(len(r.fields)))
	for _, f := range r.fields {
		b = appendName(b, f.name)
		b = f.typ.encode(b)
	}
	return b
}

type enumType struct{ cases []string }

func (e enumType) encode(b []byte) []byte {
	b = append(b, cvtEnum)
	b = appendUleb128(b, uint32(len(e.cases)))
	for _, c := range e.cases {
		b = appendName(b, c)
	}
	return b
}

type flagsType struct{ flags []string }

func (f flagsType) encode(b []byte) []byte {
	b = append(b, cvtFlags)
	b = appendUleb128(b, uint32(len(f.flags)))
	for _, name := range f.flags {
		b = appendName(b, name)
	}
	return b
}

type variantCase struct {
	name string
	typ  vtype // nil for no payload
}
type variantType struct{ cases []variantCase }

func (v variantType) encode(b []byte) []byte {
	b = append(b, cvtVariant)
	b = appendUleb128(b, uint32(len(v.cases)))
	for _, c := range v.cases {
		b = appendName(b, c.name)
		if c.typ != nil {
			b = append(b, 0x01) // some
			b = c.typ.encode(b)
		} else {
			b = append(b, 0x00) // none
		}
		b = append(b, 0x00) // refines (reserved, always 0x00)
	}
	return b
}

type optionType struct{ inner vtype }

func (o optionType) encode(b []byte) []byte {
	b = append(b, cvtOption)
	return o.inner.encode(b)
}

// resultType represents result<ok, err>. Either can be nil.
type resultType struct {
	ok  vtype
	err vtype
}

func (r resultType) encode(b []byte) []byte {
	b = append(b, cvtResult)
	if r.ok != nil {
		b = append(b, 0x01) // some ok
		b = r.ok.encode(b)
	} else {
		b = append(b, 0x00) // no ok
	}
	if r.err != nil {
		b = append(b, 0x01) // some err
		b = r.err.encode(b)
	} else {
		b = append(b, 0x00) // no err
	}
	return b
}

// typeRef references a type by local index within an instance type.
// In the component model, type references in valtype position are encoded as
// signed LEB128 (s33) where negative values are primitive types. Type indices
// must be encoded with enough bytes to keep the sign bit clear.
type typeRef uint32

func (t typeRef) encode(b []byte) []byte {
	v := int64(t)
	for {
		byt := byte(v & 0x7f)
		v >>= 7
		if v != 0 || (byt&0x40) != 0 {
			byt |= 0x80
			b = append(b, byt)
		} else {
			b = append(b, byt)
			break
		}
	}
	return b
}

type ownType struct{ typ vtype }

func (o ownType) encode(b []byte) []byte {
	b = append(b, cvtOwn)
	return o.typ.encode(b)
}

type borrowType struct{ typ vtype }

func (bt borrowType) encode(b []byte) []byte {
	b = append(b, cvtBorrow)
	return bt.typ.encode(b)
}

type streamType struct{ elem vtype } // elem can be nil for stream<>

func (s streamType) encode(b []byte) []byte {
	b = append(b, cvtStream)
	if s.elem != nil {
		b = append(b, 0x01) // some
		b = s.elem.encode(b)
	} else {
		b = append(b, 0x00)
	}
	return b
}

type futureType struct{ elem vtype }

func (f futureType) encode(b []byte) []byte {
	b = append(b, cvtFuture)
	if f.elem != nil {
		b = append(b, 0x01)
		b = f.elem.encode(b)
	} else {
		b = append(b, 0x00)
	}
	return b
}

// funcSig represents a component function signature.
type funcSig struct {
	async  bool
	params []funcParam
	result vtype // nil for no result
}

type funcParam struct {
	name string
	typ  vtype
}

func (f funcSig) encode(b []byte) []byte {
	if f.async {
		b = append(b, ttFuncAsync)
	} else {
		b = append(b, ttFunc)
	}
	b = appendUleb128(b, uint32(len(f.params)))
	for _, p := range f.params {
		b = appendName(b, p.name)
		b = p.typ.encode(b)
	}
	if f.result != nil {
		b = append(b, 0x00) // has result
		b = f.result.encode(b)
	} else {
		b = append(b, 0x01, 0x00) // no result
	}
	return b
}

// instanceItem is an item within a component instance type definition.
type instanceItem interface {
	encodeItem(b []byte) []byte
}

// instTypeDef defines a type within the instance.
type instTypeDef struct{ typ vtype }

func (d instTypeDef) encodeItem(b []byte) []byte {
	b = append(b, itType)
	return d.typ.encode(b)
}

// instFuncDef defines a function type within the instance.
type instFuncDef struct{ sig funcSig }

func (d instFuncDef) encodeItem(b []byte) []byte {
	b = append(b, itType)
	return d.sig.encode(b)
}

// instResource defines a resource type within the instance (for concrete components).
type instResource struct{}

func (instResource) encodeItem(b []byte) []byte {
	b = append(b, itType)
	b = append(b, ttResource)
	b = append(b, 0x7f) // rep i32
	b = append(b, 0x00) // no destructor
	return b
}

// instResourceExport declares a resource as an export with (sub resource) typebound.
// Used in import instance types where resources cannot be defined inline.
type instResourceExport struct {
	name string
}

func (e instResourceExport) encodeItem(b []byte) []byte {
	b = append(b, itExportFunc)
	b = append(b, 0x00) // exportname' kind: kebab
	b = appendName(b, e.name)
	b = append(b, 0x03) // externdesc: type
	b = append(b, 0x01) // typebound: sub resource
	return b
}

// instExport exports a named item from the instance.
type instExport struct {
	name string
	sort byte   // 0x01=func, 0x03=type
	idx  uint32 // type or func index within this instance
}

func (e instExport) encodeItem(b []byte) []byte {
	b = append(b, itExportFunc)
	b = append(b, 0x00) // exportname' kind: kebab
	b = appendName(b, e.name)
	b = append(b, e.sort)
	if e.sort == 0x03 {
		// Type export uses typebound: 0x00 = eq
		b = append(b, 0x00)
	}
	b = appendUleb128(b, e.idx)
	return b
}

// instAlias creates an outer alias to a type from the enclosing scope.
type instAlias struct {
	outerCount uint32 // typically 1
	outerIdx   uint32 // type index in the outer scope
}

func (a instAlias) encodeItem(b []byte) []byte {
	b = append(b, itAlias)
	b = append(b, 0x03) // sort: type
	b = append(b, 0x02) // kind: outer
	b = appendUleb128(b, a.outerCount)
	b = appendUleb128(b, a.outerIdx)
	return b
}

// instanceTypeDef encodes a full instance type (tag 0x42).
func encodeInstanceType(items []instanceItem) []byte {
	var b []byte
	b = append(b, ttInstanceType)
	b = appendUleb128(b, uint32(len(items)))
	for _, item := range items {
		b = item.encodeItem(b)
	}
	return b
}
