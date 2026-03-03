// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build witgen

package main

import (
	"fmt"
	"strings"
)

func (g *Generator) generateType(w *strings.Builder, rt *ResolvedType) {
	if rt.Name == "" {
		return
	}
	// Use the renamed name if there's a collision.
	goName := witToGoName(rt.Name)
	if renamed, ok := g.typeRenames[rt]; ok {
		goName = renamed
	}

	// Cross-file dedup: skip types already emitted by an earlier file.
	if g.generatedTypes[goName] {
		return
	}
	g.generatedTypes[goName] = true

	switch rt.Kind {
	case KindEnum:
		g.generateEnum(w, goName, rt)
	case KindRecord:
		g.generateRecord(w, goName, rt)
	case KindTuple:
		g.generateTuple(w, goName, rt)
	case KindVariant:
		g.generateVariant(w, goName, rt)
	case KindFlags:
		g.generateFlags(w, goName, rt)
	case KindResource:
		fmt.Fprintf(w, "// %s is a handle to the %s resource.\n", goName, rt.Name)
		fmt.Fprintf(w, "type %s int32\n\n", goName)
	case KindTypeAlias:
		if rt.Alias != nil {
			target := g.deref(rt.Alias)
			if target.Kind == KindPrimitive {
				fmt.Fprintf(w, "type %s = %s\n\n", goName, primToGoType(target.Primitive))
			}
			// Skip aliases to named types from other interfaces (re-exports).
		}
	}
}

func (g *Generator) generateEnum(w *strings.Builder, goName string, rt *ResolvedType) {
	fmt.Fprintf(w, "// %s represents the %s enum.\n", goName, rt.Name)
	underlying := "uint8"
	if len(rt.Cases) > 256 {
		underlying = "uint16"
	}
	fmt.Fprintf(w, "type %s %s\n\n", goName, underlying)
	fmt.Fprintf(w, "const (\n")
	for i, c := range rt.Cases {
		caseName := goName + witToGoName(c.Name)
		if i == 0 {
			fmt.Fprintf(w, "\t%s %s = iota\n", caseName, goName)
		} else {
			fmt.Fprintf(w, "\t%s\n", caseName)
		}
	}
	fmt.Fprintf(w, ")\n\n")
}

func (g *Generator) generateRecord(w *strings.Builder, goName string, rt *ResolvedType) {
	fmt.Fprintf(w, "// %s represents the %s record.\n", goName, rt.Name)
	fmt.Fprintf(w, "type %s struct {\n", goName)
	for _, f := range rt.Fields {
		fieldName := witToGoName(f.Name)
		fieldType := g.goSafeType(f.Type)
		fmt.Fprintf(w, "\t%s %s\n", fieldName, fieldType)
	}
	fmt.Fprintf(w, "}\n\n")
}

func (g *Generator) generateVariant(w *strings.Builder, goName string, rt *ResolvedType) {
	size, _ := g.abiSizeOfVariant(rt.Cases)
	discSz, _ := g.discSize(len(rt.Cases))

	fmt.Fprintf(w, "// %s represents the %s variant (ABI size: %d bytes).\n", goName, rt.Name, size)
	fmt.Fprintf(w, "type %s struct {\n", goName)
	discType := "uint8"
	if discSz == 2 {
		discType = "uint16"
	} else if discSz >= 4 {
		discType = "uint32"
	}
	fmt.Fprintf(w, "\tdisc %s\n", discType)

	// Compute payload padding and size.
	padBytes := g.variantPayloadOffset(rt.Cases) - discSz

	payloadSize := size - discSz
	if payloadSize > 0 {
		fmt.Fprintf(w, "\tdata [%d]byte\n", payloadSize)
	}
	fmt.Fprintf(w, "}\n\n")

	// Generate case constants.
	fmt.Fprintf(w, "const (\n")
	for i, c := range rt.Cases {
		caseName := goName + witToGoName(c.Name)
		if i == 0 {
			fmt.Fprintf(w, "\t%s %s = iota\n", caseName, discType)
		} else {
			fmt.Fprintf(w, "\t%s\n", caseName)
		}
	}
	fmt.Fprintf(w, ")\n\n")

	// Generate Disc() getter.
	fmt.Fprintf(w, "func (v %s) Disc() %s { return v.disc }\n\n", goName, discType)

	// Generate typed accessor and setter methods for each case.
	for i, c := range rt.Cases {
		caseGoName := witToGoName(c.Name)

		if c.Type == nil {
			// No-payload case: generate setter only (no accessor).
			fmt.Fprintf(w, "func (v *%s) Set%s() {\n", goName, caseGoName)
			fmt.Fprintf(w, "\tv.disc = %d\n", i)
			fmt.Fprintf(w, "}\n\n")
			continue
		}

		if payloadSize == 0 {
			continue
		}

		crt := g.deref(c.Type)
		if crt.Name == "" {
			continue
		}
		if crt.Kind != KindRecord && crt.Kind != KindTuple {
			continue
		}
		if !g.isTypeAvailable(c.Type) {
			continue
		}
		caseGoType := g.goType(c.Type)
		// Accessor: return by value with discriminant check.
		g.needsUnsafe = true
		constName := goName + caseGoName
		fmt.Fprintf(w, "func (v %s) %s() %s {\n", goName, caseGoName, caseGoType)
		fmt.Fprintf(w, "\tif v.disc != %s { panic(\"%s: not %s\") }\n", constName, goName, caseGoName)
		fmt.Fprintf(w, "\treturn *(*%s)(unsafe.Pointer(&v.data[%d]))\n", caseGoType, padBytes)
		fmt.Fprintf(w, "}\n\n")

		// Setter: atomically set disc + payload, if GC-safe.
		if g.isGCSafe(c.Type) {
			fmt.Fprintf(w, "func (v *%s) Set%s(val %s) {\n", goName, caseGoName, caseGoType)
			fmt.Fprintf(w, "\tv.disc = %d\n", i)
			fmt.Fprintf(w, "\t*(*%s)(unsafe.Pointer(&v.data[%d])) = val\n", caseGoType, padBytes)
			fmt.Fprintf(w, "}\n\n")
		}
	}
}

// isGCSafe reports whether a type contains no string, list, or other
// GC-traceable types at any depth. Variant Set*() methods can only be
// generated when all case payloads are GC-safe, because the GC cannot
// trace pointers inside variant data [N]byte fields.
func (g *Generator) isGCSafe(rt *ResolvedType) bool {
	if rt == nil {
		return true
	}
	rt = g.deref(rt)
	switch rt.Kind {
	case KindPrimitive:
		return rt.Primitive != "string"
	case KindEnum, KindFlags, KindHandle, KindResource:
		return true
	case KindRecord, KindTuple:
		for _, f := range rt.Fields {
			if !g.isGCSafe(f.Type) {
				return false
			}
		}
		return true
	case KindVariant:
		for _, c := range rt.Cases {
			if !g.isGCSafe(c.Type) {
				return false
			}
		}
		return true
	case KindList, KindStream, KindFuture:
		return false
	case KindOption:
		return g.isGCSafe(rt.Inner)
	case KindResult:
		return g.isGCSafe(rt.Ok) && g.isGCSafe(rt.Err)
	}
	return false
}

func (g *Generator) generateFlags(w *strings.Builder, goName string, rt *ResolvedType) {
	baseType := "uint8"
	if len(rt.Flags) > 8 {
		baseType = "uint16"
	}
	if len(rt.Flags) > 16 {
		baseType = "uint32"
	}

	fmt.Fprintf(w, "// %s represents the %s flags.\n", goName, rt.Name)
	fmt.Fprintf(w, "type %s %s\n\n", goName, baseType)
	fmt.Fprintf(w, "const (\n")
	for i, f := range rt.Flags {
		flagName := goName + witToGoName(f)
		fmt.Fprintf(w, "\t%s %s = 1 << %d\n", flagName, goName, i)
	}
	fmt.Fprintf(w, ")\n\n")
}

func (g *Generator) generateTuple(w *strings.Builder, goName string, rt *ResolvedType) {
	fmt.Fprintf(w, "// %s represents the %s tuple.\n", goName, rt.Name)
	fmt.Fprintf(w, "type %s struct {\n", goName)
	for i, f := range rt.Fields {
		fieldName := fmt.Sprintf("F%d", i)
		fieldType := g.goType(f.Type)
		fmt.Fprintf(w, "\t%s %s\n", fieldName, fieldType)
	}
	fmt.Fprintf(w, "}\n\n")
}

// maybeGenerateResultStruct generates a result<ok, err> type with inline
// [N]byte storage and typed accessor methods.
func (g *Generator) maybeGenerateResultStruct(w *strings.Builder, rt *ResolvedType) {
	rt = g.deref(rt)
	if rt.Kind != KindResult {
		return
	}

	structName := g.resultStructName(rt)
	if g.generatedResults[structName] {
		return
	}
	g.generatedResults[structName] = true
	g.needsWasiImport = true
	g.needsUnsafe = true

	size, _ := g.abiSize(rt)

	// Compute payload offset after discriminant + padding.
	// Result is a 2-case variant: ok(Ok) and err(Err).
	resultCases := []Case{{Name: "ok", Type: rt.Ok}, {Name: "err", Type: rt.Err}}
	payloadOff := g.variantPayloadOffset(resultCases)

	okDesc := g.typeDescStr(rt.Ok)
	errDesc := g.typeDescStr(rt.Err)

	fmt.Fprintf(w, "// %s wraps result<%s, %s> (%d bytes).\n",
		structName, okDesc, errDesc, size)
	fmt.Fprintf(w, "// Uses inline storage — no heap allocation on return.\n")
	fmt.Fprintf(w, "type %s struct { _ [0]int64; b [%d]byte }\n\n", structName, size)
	fmt.Fprintf(w, "const %sSize = %d\n\n", structName, size)

	fmt.Fprintf(w, "func (r %s) IsErr() bool { return r.b[0] != 0 }\n\n", structName)

	// Err() accessor.
	g.generateResultCaseAccessor(w, structName, "Err", rt.Err, payloadOff, "r.b[0] == 0", structName+": result is ok")

	// OK() accessor.
	if rt.Ok != nil {
		okRT := g.deref(rt.Ok)
		if (okRT.Kind == KindRecord || okRT.Kind == KindTuple) && okRT.Name == "" {
			g.generateResultTupleOK(w, structName, okRT, payloadOff)
		} else {
			g.generateResultCaseAccessor(w, structName, "OK", rt.Ok, payloadOff, "r.b[0] != 0", structName+": result is err")
		}
	}
}

// generateResultCaseAccessor generates a typed accessor method (e.g. OK() or Err())
// on a result struct for a single variant case.
//   - structName: the result struct (e.g. "ResultDescriptorErrorCode")
//   - methodName: accessor name (e.g. "OK" or "Err")
//   - rt: the payload type for this case (nil = no payload)
//   - payloadOff: byte offset of the payload in the result's inline [N]byte storage
//   - panicCond/panicMsg: guard expression and message when the wrong case is accessed
func (g *Generator) generateResultCaseAccessor(w *strings.Builder, structName, methodName string, rt *ResolvedType, payloadOff int, panicCond, panicMsg string) {
	if rt == nil {
		return
	}
	derefRT := g.deref(rt)
	ba := BufField("r.b")

	// String: pointer receiver + CopyString + zero-on-read.
	if g.isPrim(rt, "string") {
		g.needsWasiImport = true
		fmt.Fprintf(w, "func (r *%s) %s() string {\n", structName, methodName)
		fmt.Fprintf(w, "\tif %s { panic(\"%s\") }\n", panicCond, panicMsg)
		fmt.Fprintf(w, "\tptr := *(*int32)(%s)\n", ba.ptrAt(payloadOff))
		fmt.Fprintf(w, "\tlength := *(*int32)(%s)\n", ba.ptrAt(payloadOff+4))
		fmt.Fprintf(w, "\t*(*int32)(%s) = 0\n", ba.ptrAt(payloadOff))
		fmt.Fprintf(w, "\treturn wasi.CopyString(ptr, length)\n")
		fmt.Fprintf(w, "}\n\n")
		return
	}

	// Named record/tuple: read fields into struct.
	if (derefRT.Kind == KindRecord || derefRT.Kind == KindTuple) && derefRT.Name != "" {
		goType := g.goType(rt)
		fmt.Fprintf(w, "func (r %s) %s() %s {\n", structName, methodName, goType)
		fmt.Fprintf(w, "\tif %s { panic(\"%s\") }\n", panicCond, panicMsg)
		g.generateRecordFieldsReturn(w, derefRT, goType, payloadOff)
		fmt.Fprintf(w, "}\n\n")
		return
	}

	// Option: decode with discriminant + payload.
	if derefRT.Kind == KindOption {
		goType := g.goSafeType(rt)
		fmt.Fprintf(w, "func (r %s) %s() %s {\n", structName, methodName, goType)
		fmt.Fprintf(w, "\tif %s { panic(\"%s\") }\n", panicCond, panicMsg)
		g.generateOptionDecodeB(w, derefRT, payloadOff, "")
		fmt.Fprintf(w, "}\n\n")
		return
	}

	// Simple types: primitive, enum, flags, handle, resource, variant, etc.
	if expr, ok := g.emitReadExpr(rt, ba, payloadOff); ok {
		goType := g.goType(rt)
		fmt.Fprintf(w, "func (r %s) %s() %s {\n", structName, methodName, goType)
		fmt.Fprintf(w, "\tif %s { panic(\"%s\") }\n", panicCond, panicMsg)
		fmt.Fprintf(w, "\treturn %s\n", expr)
		fmt.Fprintf(w, "}\n\n")
	}
}

// generateResultTupleOK generates a multi-return OK() method for an anonymous
// tuple inside a result. E.g. func (r ResultXxx) OK() (wasi.ByteBuffer, IPSocketAddress).
func (g *Generator) generateResultTupleOK(w *strings.Builder, structName string, rt *ResolvedType, baseOff int) {
	panicMsg := fmt.Sprintf("%s: result is err", structName)
	ba := BufField("r.b")

	// Build return type list and read expressions.
	var retTypes []string
	var retExprs []string
	offset := baseOff
	for _, f := range rt.Fields {
		fs, fa := g.abiSize(f.Type)
		offset = alignTo(offset, fa)

		if f.Type == nil {
			offset += fs
			continue
		}

		fGoType := g.goType(f.Type)
		frt := g.deref(f.Type)
		if frt.Kind == KindPrimitive {
			fGoType = primToGoType(frt.Primitive)
		}
		retTypes = append(retTypes, fGoType)

		expr, ok := g.emitReadExpr(f.Type, ba, offset)
		if !ok {
			expr = loadB(fGoType, offset, fs)
		}
		retExprs = append(retExprs, expr)

		offset += fs
	}

	retTypeStr := strings.Join(retTypes, ", ")
	if len(retTypes) > 1 {
		retTypeStr = "(" + retTypeStr + ")"
	}

	fmt.Fprintf(w, "func (r %s) OK() %s {\n", structName, retTypeStr)
	fmt.Fprintf(w, "\tif r.b[0] != 0 { panic(\"%s\") }\n", panicMsg)
	fmt.Fprintf(w, "\treturn %s\n", strings.Join(retExprs, ", "))
	fmt.Fprintf(w, "}\n\n")
}

// generateOptionDecodeB decodes an option<T> from r.b. When assignTarget is
// set, generates a field assignment; otherwise generates a return statement.
func (g *Generator) generateOptionDecodeB(w *strings.Builder, optRT *ResolvedType, baseOff int, assignTarget string) {
	discOff := baseOff
	var payloadAlign int
	if optRT.Inner != nil {
		_, payloadAlign = g.abiSize(optRT.Inner)
	} else {
		payloadAlign = 1
	}
	payloadOff := alignTo(discOff+1, payloadAlign)

	returnMode := assignTarget == ""
	indent := "\t\t"
	if returnMode {
		indent = "\t"
		fmt.Fprintf(w, "\tif r.b[%d] == 0 {\n\t\treturn nil\n\t}\n", discOff)
	} else {
		fmt.Fprintf(w, "\tif r.b[%d] != 0 {\n", discOff)
	}

	// assignValue emits "v := expr" then either "return &v" or "target = &v".
	assignValue := func(expr string) {
		fmt.Fprintf(w, "%sv := %s\n", indent, expr)
		if returnMode {
			fmt.Fprintf(w, "%sreturn &v\n", indent)
		} else {
			fmt.Fprintf(w, "%s%s = &v\n", indent, assignTarget)
		}
	}

	ba := BufField("r.b")
	if optRT.Inner != nil {
		innerRT := g.deref(optRT.Inner)
		innerGoType := g.goType(optRT.Inner)

		if g.isPrim(optRT.Inner, "string") {
			g.needsWasiImport = true
			assignValue(fmt.Sprintf("wasi.CopyString(*(*int32)(%s), *(*int32)(%s))", ba.ptrAt(payloadOff), ba.ptrAt(payloadOff+4)))
		} else if expr, ok := g.emitReadExpr(optRT.Inner, ba, payloadOff); ok {
			assignValue(expr)
		} else if innerRT.Kind == KindRecord || innerRT.Kind == KindTuple {
			if returnMode {
				fmt.Fprintf(w, "\tv := %s{\n", innerGoType)
				g.emitRecordFieldsInline(w, innerRT, ba, payloadOff, "\t\t")
				fmt.Fprintf(w, "\t}\n")
				fmt.Fprintf(w, "\treturn &v\n")
			} else {
				fmt.Fprintf(w, "\t\t%s = &%s{\n", assignTarget, innerGoType)
				g.emitRecordFieldsInline(w, innerRT, ba, payloadOff, "\t\t\t")
				fmt.Fprintf(w, "\t\t}\n")
			}
		} else if returnMode {
			fmt.Fprintf(w, "\t// TODO: option<%s> decode\n", innerGoType)
			fmt.Fprintf(w, "\treturn nil\n")
		} else {
			fmt.Fprintf(w, "\t\t// TODO: option<%s> decode\n", innerGoType)
		}
	} else if returnMode {
		fmt.Fprintf(w, "\treturn nil\n")
	}

	if !returnMode {
		fmt.Fprintf(w, "\t}\n")
	}
}

// generateRecordFieldsReturn generates a return statement decoding a record from r.b.
func (g *Generator) generateRecordFieldsReturn(w *strings.Builder, rt *ResolvedType, goType string, baseOff int) {
	type fieldInfo struct {
		name   string
		offset int
		size   int
		simple bool
		expr   string
	}

	ba := BufField("r.b")
	offset := baseOff
	var fields []fieldInfo
	hasComplex := false
	for _, f := range rt.Fields {
		fieldName := witToGoName(f.Name)
		fs, fa := g.abiSize(f.Type)
		offset = alignTo(offset, fa)
		fi := fieldInfo{name: fieldName, offset: offset, size: fs, simple: true}

		frt := g.deref(f.Type)
		if g.isPrim(f.Type, "string") {
			g.needsWasiImport = true
			fi.expr = fmt.Sprintf("wasi.CopyString(*(*int32)(%s), *(*int32)(%s))", ba.ptrAt(offset), ba.ptrAt(offset+4))
		} else if expr, ok := g.emitReadExpr(f.Type, ba, offset); ok {
			fi.expr = expr
		} else if frt.Kind == KindOption || frt.Kind == KindRecord || frt.Kind == KindTuple {
			fi.simple = false
			hasComplex = true
		} else {
			fGoType := g.goType(f.Type)
			fi.expr = loadB(fGoType, offset, fs)
		}
		fields = append(fields, fi)
		offset += fs
	}

	if !hasComplex {
		fmt.Fprintf(w, "\treturn %s{\n", goType)
		for _, fi := range fields {
			fmt.Fprintf(w, "\t\t%s: %s,\n", fi.name, fi.expr)
		}
		fmt.Fprintf(w, "\t}\n")
	} else {
		fmt.Fprintf(w, "\ts := %s{\n", goType)
		for _, fi := range fields {
			if fi.simple {
				fmt.Fprintf(w, "\t\t%s: %s,\n", fi.name, fi.expr)
			}
		}
		fmt.Fprintf(w, "\t}\n")
		for i, fi := range fields {
			if fi.simple {
				continue
			}
			f := rt.Fields[i]
			frt := g.deref(f.Type)
			switch frt.Kind {
			case KindOption:
				g.generateOptionDecodeB(w, frt, fi.offset, "s."+fi.name)
			case KindRecord, KindTuple:
				fGoType := g.goType(f.Type)
				fmt.Fprintf(w, "\ts.%s = %s{\n", fi.name, fGoType)
				g.emitRecordFieldsInline(w, frt, BufField("r.b"), fi.offset, "\t\t")
				fmt.Fprintf(w, "\t}\n")
			}
		}
		fmt.Fprintf(w, "\treturn s\n")
	}
}
