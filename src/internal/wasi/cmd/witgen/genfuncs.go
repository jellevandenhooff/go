// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build witgen

package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// --- Function generation ---

func (g *Generator) generateFunc(w *strings.Builder, fn Function, iface Interface) {
	rf := g.resolveFunc(fn)

	// Build the wasmimport module path.
	modulePath := g.modulePath(iface)

	// Determine Go function name.
	goFuncName := g.goFuncName(rf)

	// Determine parameter and return types based on canonical ABI.
	g.generateFuncDecl(w, rf, modulePath, goFuncName)
}

func (g *Generator) resolveFunc(fn Function) ResolvedFunc {
	rf := ResolvedFunc{
		Name: fn.Name,
	}

	// Parse kind.
	var kindStr string
	if err := json.Unmarshal(fn.Kind, &kindStr); err == nil {
		switch kindStr {
		case "freestanding":
			rf.Kind = FuncFreestanding
		case "async-freestanding":
			rf.Kind = FuncAsyncFreestanding
			rf.IsAsync = true
		}
	} else {
		var kindObj map[string]int
		json.Unmarshal(fn.Kind, &kindObj)
		if v, ok := kindObj["method"]; ok {
			rf.Kind = FuncMethod
			rf.Resource = &g.types[v]
		} else if v, ok := kindObj["static"]; ok {
			rf.Kind = FuncStatic
			rf.Resource = &g.types[v]
		} else if v, ok := kindObj["constructor"]; ok {
			rf.Kind = FuncConstructor
			rf.Resource = &g.types[v]
		} else if v, ok := kindObj["async-method"]; ok {
			rf.Kind = FuncAsyncMethod
			rf.Resource = &g.types[v]
			rf.IsAsync = true
		} else if v, ok := kindObj["async-static"]; ok {
			rf.Kind = FuncAsyncStatic
			rf.Resource = &g.types[v]
			rf.IsAsync = true
		}
	}

	// Parse params.
	for _, p := range fn.Params {
		rf.Params = append(rf.Params, ResolvedParam{
			Name: p.Name,
			Type: g.resolveTypeRef(p.Type),
		})
	}

	// Parse result.
	if fn.Result != nil && string(fn.Result) != "null" {
		rf.Result = g.resolveTypeRef(fn.Result)
	}

	return rf
}

// isMethod reports whether a function kind is a method or async-method.
func isMethod(kind FuncKind) bool {
	return kind == FuncMethod || kind == FuncAsyncMethod
}

// needsWrapper reports whether a function needs a public Go wrapper.
func (g *Generator) needsWrapper(rf ResolvedFunc) bool {
	if isMethod(rf.Kind) {
		return true
	}
	if rf.Result == nil {
		return false
	}
	rt := g.deref(rf.Result)
	if rt.Kind == KindResult {
		flatResults := g.flatten(rf.Result)
		return len(flatResults) > maxFlatResults || rf.IsAsync
	}
	if rt.Kind == KindList || rt.Kind == KindOption {
		return true
	}
	if rt.Kind == KindRecord && rt.Name != "" {
		flatResults := g.flatten(rf.Result)
		return len(flatResults) > maxFlatResults
	}
	if rt.Kind == KindFuture {
		return true
	}
	if rt.Kind == KindTuple {
		for _, f := range rt.Fields {
			if f.Type != nil {
				ft := g.deref(f.Type)
				if ft.Kind == KindStream || ft.Kind == KindFuture {
					return true
				}
			}
		}
	}
	return false
}

// paramsTupleSize computes the ABI tuple size of a function's parameters.
func (g *Generator) paramsTupleSize(rf ResolvedFunc) int {
	offset := 0
	maxAlign := 1
	for _, p := range rf.Params {
		ps, pa := g.abiSize(p.Type)
		if pa > maxAlign {
			maxAlign = pa
		}
		offset = alignTo(offset, pa)
		offset += ps
	}
	return alignTo(offset, maxAlign)
}

// emitPrimStore stores a primitive value from a flat param into params.b.
func (g *Generator) emitPrimStore(w *strings.Builder, expr, prim string, offset int) {
	p, ok := primInfo[prim]
	if !ok {
		return
	}
	fmt.Fprintf(w, "\t%s\n", storeStmt("params.b", p.goType, expr, offset, p.size))
}

// generateTypeStore stores flat param values into a params_ptr buffer.
func (g *Generator) generateTypeStore(w *strings.Builder, rt *ResolvedType, offset int, flatParamNames []string, flatIdx int) int {
	rt = g.deref(rt)
	switch rt.Kind {
	case KindPrimitive:
		if rt.Primitive == "string" {
			// String has 2 flat params: ptr and len, stored as (u32, u32).
			fmt.Fprintf(w, "\t*(*int32)(unsafe.Pointer(&params.b[%d])) = %s\n", offset, flatParamNames[flatIdx])
			fmt.Fprintf(w, "\t*(*int32)(unsafe.Pointer(&params.b[%d])) = %s\n", offset+4, flatParamNames[flatIdx+1])
			return flatIdx + 2
		}
		g.emitPrimStore(w, flatParamNames[flatIdx], rt.Primitive, offset)
		return flatIdx + 1
	case KindTypeAlias:
		if rt.Alias != nil {
			return g.generateTypeStore(w, rt.Alias, offset, flatParamNames, flatIdx)
		}
		g.emitPrimStore(w, flatParamNames[flatIdx], rt.Primitive, offset)
		return flatIdx + 1
	case KindHandle, KindResource, KindStream, KindFuture:
		fmt.Fprintf(w, "\t*(*int32)(unsafe.Pointer(&params.b[%d])) = %s\n", offset, flatParamNames[flatIdx])
		return flatIdx + 1
	case KindEnum, KindFlags:
		sz, _ := g.abiSize(rt)
		fmt.Fprintf(w, "\t%s\n", storeStmt("params.b", sizedType(sz), flatParamNames[flatIdx], offset, sz))
		return flatIdx + 1
	case KindRecord, KindTuple:
		innerOff := offset
		for _, f := range rt.Fields {
			fs, fa := g.abiSize(f.Type)
			innerOff = alignTo(innerOff, fa)
			flatIdx = g.generateTypeStore(w, f.Type, innerOff, flatParamNames, flatIdx)
			innerOff += fs
		}
		return flatIdx
	case KindList:
		// List is (ptr, len) like string.
		fmt.Fprintf(w, "\t*(*int32)(unsafe.Pointer(&params.b[%d])) = %s\n", offset, flatParamNames[flatIdx])
		fmt.Fprintf(w, "\t*(*int32)(unsafe.Pointer(&params.b[%d])) = %s\n", offset+4, flatParamNames[flatIdx+1])
		return flatIdx + 2
	case KindOption:
		// Option is a 2-case variant: none (empty) and some(inner).
		// Flat params: disc + flat(inner)
		// Memory layout: disc(u8) + padding + inner payload
		cases := []Case{
			{Name: "none"},
			{Name: "some", Type: rt.Inner},
		}
		return g.generateVariantStore(w, rt, cases, offset, flatParamNames, flatIdx)
	case KindVariant:
		return g.generateVariantStore(w, rt, rt.Cases, offset, flatParamNames, flatIdx)
	}

	// Fallback: store as i32.
	fmt.Fprintf(w, "\t*(*int32)(unsafe.Pointer(&params.b[%d])) = %s\n", offset, flatParamNames[flatIdx])
	return flatIdx + 1
}

// generateVariantStore emits stores for a variant type (including option/result)
// from flat param values into the params buffer.
func (g *Generator) generateVariantStore(w *strings.Builder, rt *ResolvedType, cases []Case, offset int, flatParamNames []string, flatIdx int) int {
	// Variant: disc + padding + case union.
	// For params_ptr packing, we write all flat values (disc + max case fields).
	discSz, _ := g.discSize(len(cases))
	payloadOff := g.variantPayloadOffset(cases)

	// Store discriminant.
	fmt.Fprintf(w, "\t%s\n", storeStmt("params.b", sizedType(discSz), flatParamNames[flatIdx], offset, discSz))
	flatIdx++

	// Store payload flat values using the max case's ABI layout.
	// Find the max case (most flat values) to determine payload ABI layout.
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

	if maxCaseIdx >= 0 && maxCaseFlat > 0 {
		// Map flat payload values to ABI byte offsets using the max case's type layout.
		mc := cases[maxCaseIdx]
		abiMapping := g.flatToABIMapping(mc.Type, offset+payloadOff)
		// There may be padding flat values beyond the max case's fields
		// (from other cases with fewer fields). Those are zeros and can be ignored.
		flatPayload := g.flatten(rt)
		payloadFlats := len(flatPayload) - 1 // minus discriminant
		for i := 0; i < payloadFlats; i++ {
			if i < len(abiMapping) {
				m := abiMapping[i]
				fmt.Fprintf(w, "\t%s\n", storeStmt("params.b", sizedType(m.size), flatParamNames[flatIdx], m.offset, m.size))
			}
			// else: padding flat values, skip store
			flatIdx++
		}
	}
	return flatIdx
}

// emitStoreValue emits a Go statement storing expr into bufName[offset] for
// the given resolved type. Handles primitives, handles/resources/streams/futures,
// enums/flags, records/tuples (recursive field walk), and variants (struct copy).
// String and list types must be decomposed by the caller.
func (g *Generator) emitStoreValue(w *strings.Builder, origRT *ResolvedType, bufName string, offset int, expr string) {
	rt := g.deref(origRT)
	switch rt.Kind {
	case KindPrimitive:
		p, ok := primInfo[rt.Primitive]
		if !ok {
			return
		}
		fmt.Fprintf(w, "\t%s\n", storeStmt(bufName, p.goType, expr, offset, p.size))
	case KindHandle, KindResource, KindStream, KindFuture:
		fmt.Fprintf(w, "\t%s\n", storeStmt(bufName, "int32", expr, offset, 4))
	case KindEnum, KindFlags:
		sz, _ := g.abiSize(rt)
		fmt.Fprintf(w, "\t%s\n", storeStmt(bufName, sizedType(sz), expr, offset, sz))
	case KindRecord, KindTuple:
		innerOff := offset
		for _, f := range rt.Fields {
			fs, fa := g.abiSize(f.Type)
			innerOff = alignTo(innerOff, fa)
			fieldExpr := expr + "." + witToGoName(f.Name)
			g.emitStoreValue(w, f.Type, bufName, innerOff, fieldExpr)
			innerOff += fs
		}
	case KindVariant:
		goType := g.goType(origRT)
		fmt.Fprintf(w, "\t*(*%s)(unsafe.Pointer(&%s[%d])) = %s\n", goType, bufName, offset, expr)
	}
}

// flatParamDecls returns Go parameter declarations for flat params.
// If there's a single flat value, it uses just the name; otherwise it
// appends a numeric suffix (name0, name1, ...).
func flatParamDecls(name string, flat []WasmValType) []string {
	if len(flat) == 1 {
		return []string{fmt.Sprintf("%s %s", name, flat[0].GoType())}
	}
	decls := make([]string, len(flat))
	for i, f := range flat {
		decls[i] = fmt.Sprintf("%s%d %s", name, i, f.GoType())
	}
	return decls
}

func (g *Generator) generateFuncDecl(w *strings.Builder, rf ResolvedFunc, modulePath, goFuncName string) {
	g.needsUnsafe = true
	// Count flat params.
	var flatParams []WasmValType
	for _, p := range rf.Params {
		flatParams = append(flatParams, g.flatten(p.Type)...)
	}

	// Count flat results.
	var flatResults []WasmValType
	if rf.Result != nil {
		flatResults = g.flatten(rf.Result)
	}

	// Build the wasmimport name (including [async-lower] etc.)
	wasmName := rf.Name
	asyncPrefix := ""
	if rf.IsAsync {
		asyncPrefix = "[async-lower]"
	}

	// Determine calling convention.
	paramLimit := maxFlatParams
	if rf.IsAsync {
		paramLimit = maxFlatAsyncParams
	}
	useParamsPtr := len(flatParams) > paramLimit
	useRetptr := len(flatResults) > maxFlatResults || rf.IsAsync
	hasReturn := !rf.IsAsync && len(flatResults) <= maxFlatResults && len(flatResults) > 0

	// Check if we should generate a wrapper.
	wrap := g.needsWrapper(rf)

	// For methods, the raw wasmimport function is always unexported
	// and uses the full ResourceMethod name (e.g. tCPSocketListen).
	// The public API is a method on the resource type.
	rawFuncName := goFuncName
	if wrap {
		rawFuncName = unexport(goFuncName)
	}

	// Build the individual flat param list (used for wasmimport declaration).
	var flatGoParams []string
	for _, p := range rf.Params {
		flatGoParams = append(flatGoParams, flatParamDecls(witToGoParamName(p.Name), g.flatten(p.Type))...)
	}

	// Pre-collect stream/future info for all params and result.
	var sfInfo []streamFutureInfo
	for _, p := range rf.Params {
		g.collectStreamFuture(p.Type, &sfInfo)
	}
	if rf.Result != nil {
		g.collectStreamFuture(rf.Result, &sfInfo)
	}

	// Build typed wrapper params: variant, string, list, and stream params
	// use their Go types instead of flat int32 values. Other params stay flat.
	var typedWrapperParams []string
	sfIdx := 0
	for _, p := range rf.Params {
		paramGoName := witToGoParamName(p.Name)
		rt := g.deref(p.Type)
		switch rt.Kind {
		case KindPrimitive:
			if rt.Primitive == "string" {
				typedWrapperParams = append(typedWrapperParams, paramGoName+" string")
			} else {
				typedWrapperParams = append(typedWrapperParams, flatParamDecls(paramGoName, g.flatten(p.Type))...)
			}
		case KindVariant:
			typedWrapperParams = append(typedWrapperParams, paramGoName+" "+g.goType(p.Type))
		case KindList:
			elemType := g.innerGoType(rt)
			typedWrapperParams = append(typedWrapperParams, paramGoName+" []"+elemType)
		case KindOption:
			if rt.Inner != nil {
				innerRT := g.deref(rt.Inner)
				if innerRT.Kind == KindVariant {
					typedWrapperParams = append(typedWrapperParams, paramGoName+" *"+g.goType(rt.Inner))
				} else {
					// Fall back to flat for non-variant options.
					typedWrapperParams = append(typedWrapperParams, flatParamDecls(paramGoName, g.flatten(p.Type))...)
				}
			} else {
				typedWrapperParams = append(typedWrapperParams, fmt.Sprintf("%s int32", paramGoName))
			}
		case KindStream:
			elemType := g.innerGoType(rt)
			opsName := streamOpsName(goFuncName, sfInfo[sfIdx].typeParamIdx)
			typedWrapperParams = append(typedWrapperParams,
				fmt.Sprintf("%s wasi.StreamReader[%s, %s]", paramGoName, opsName, elemType))
			sfIdx++
		case KindFuture:
			sfIdx++
			typedWrapperParams = append(typedWrapperParams, flatParamDecls(paramGoName, g.flatten(p.Type))...)
		default:
			typedWrapperParams = append(typedWrapperParams, flatParamDecls(paramGoName, g.flatten(p.Type))...)
		}
	}

	// Raw function params: use params pointer if needed, otherwise flat params.
	var goParams []string
	if useParamsPtr {
		goParams = append(goParams, "params unsafe.Pointer")
	} else {
		goParams = append(goParams, flatGoParams...)
	}
	if useRetptr {
		goParams = append(goParams, "retptr unsafe.Pointer")
	}

	var returnType string
	if rf.IsAsync {
		returnType = " int32" // async status
	} else if hasReturn {
		// Always use wasm-native types for wasmimport return values
		// (bool, char etc. must be int32 in wasmimport signatures).
		if len(flatResults) == 1 {
			returnType = " " + flatResults[0].GoType()
		}
	}

	// Emit raw wasmimport function (always noescape for the blocking variant).
	wasmImport := fmt.Sprintf("//go:wasmimport %s %s%s", modulePath, asyncPrefix, wasmName)
	paramStr := strings.Join(goParams, ", ")
	fmt.Fprintf(w, "%s\n//go:noescape\nfunc %s(%s)%s\n\n", wasmImport, rawFuncName, paramStr, returnType)
	if rf.IsAsync {
		// Async variant (no noescape): retptr may escape to heap.
		fmt.Fprintf(w, "%s\nfunc %sAsync(%s)%s\n\n", wasmImport, rawFuncName, paramStr, returnType)
	}

	// For list<record/tuple> returns, emit entry struct type before the wrapper.
	if wrap && rf.Result != nil {
		resultRT := g.deref(rf.Result)
		if resultRT.Kind == KindList && resultRT.Inner != nil {
			innerRT := g.deref(resultRT.Inner)
			if innerRT.Kind == KindTuple || innerRT.Kind == KindRecord {
				entryTypeName := goFuncName + "Entry"
				fmt.Fprintf(w, "// %s represents a list element from %s.\n", entryTypeName, rf.Name)
				fmt.Fprintf(w, "type %s struct {\n", entryTypeName)
				for i, f := range innerRT.Fields {
					var fieldName string
					if f.Name != "" {
						fieldName = witToGoName(f.Name)
					} else {
						fieldName = fmt.Sprintf("F%d", i)
					}
					fieldType := g.goSafeType(f.Type)
					fmt.Fprintf(w, "\t%s %s\n", fieldName, fieldType)
				}
				fmt.Fprintf(w, "}\n\n")
			}
		}
	}

	// Generate the public wrapper(s).
	if wrap {
		if rf.IsAsync {
			// Generate BOTH blocking and async wrappers.
			asyncRawFuncName := rawFuncName + "Async"
			// 1. Blocking wrapper: allocates result internally,
			//    uses noescape import + AsyncWait, returns result.
			g.generateFuncWrapper(w, rf, goFuncName, rawFuncName, typedWrapperParams, useParamsPtr, useRetptr, true, sfInfo)
			// 2. Async wrapper: takes *Result, uses non-noescape import,
			//    returns int32 status. Method name has "Async" suffix.
			g.generateFuncWrapper(w, rf, goFuncName+"Async", asyncRawFuncName, typedWrapperParams, useParamsPtr, useRetptr, false, sfInfo)
		} else {
			g.generateFuncWrapper(w, rf, goFuncName, rawFuncName, typedWrapperParams, useParamsPtr, useRetptr, false, sfInfo)
		}
	}
}

// Return mode determines how the return value is decoded after the wasmimport call.
type returnMode int

const (
	rmNone        returnMode = iota
	rmResult                 // result<ok, err> → copy buf to result struct
	rmList                   // list<T> → decode (ptr, len) from retptr buf
	rmOption                 // option<T> → decode disc + payload from retptr buf
	rmRecord                 // record → decode fields from retptr buf
	rmTupleStream            // tuple with stream/future → decode per-field from retptr buf
	rmFuture                 // future<T> → flat i32 return, wrap in FutureReader
)

// wrapperPlan captures all pre-computed decisions for generating a function wrapper.
// Separates "decide what to do" from "emit code" — planWrapper computes the plan,
// then generateFuncWrapper emits code from it.
type wrapperPlan struct {
	rf         ResolvedFunc
	rawName    string
	goFuncName string // full Go function name (for ops type naming)

	// Method receiver (zero values for freestanding).
	recvDecl   string // "(d Descriptor) " or ""
	recvExpr   string // "int32(d)" or ""
	methodName string // short method name for Go API
	methodFunc bool

	// Parameters.
	methodParams []string // typed method parameter declarations
	keepAlives   []string // params needing wasi.KeepAlive after the call
	startIdx     int      // 0 for freestanding, 1 for method

	// Call convention.
	useParamsPtr    bool
	hasVariantParam bool // flat mode has a variant param requiring switch dispatch

	// Return.
	returnType string
	returnMode returnMode
	retBufSize any    // int or string (const name)
	structName string // result struct name (e.g. "ResultDescriptorErrorCode")
	sizeName   string // result size const name (e.g. "ResultDescriptorErrorCodeSize")

	// Async.
	isAsync        bool
	blocking       bool
	asyncResultPtr bool

	// Stream/future info.
	sfInfo []streamFutureInfo
}

// planWrapper extracts all decision logic for a function wrapper into a wrapperPlan.
// Returns nil if no wrapper is needed.
func (g *Generator) planWrapper(rf ResolvedFunc, wrapperName, rawName string, wrapperParams []string, useParamsPtr, useRetptr, blocking bool, sfInfo []streamFutureInfo) *wrapperPlan {
	plan := &wrapperPlan{
		rf:           rf,
		rawName:      rawName,
		goFuncName:   g.goFuncName(rf),
		methodFunc:   isMethod(rf.Kind),
		useParamsPtr: useParamsPtr,
		isAsync:      rf.IsAsync,
		blocking:     blocking,
		sfInfo:       sfInfo,
	}

	// Set up method receiver.
	plan.methodName = wrapperName
	if plan.methodFunc {
		recvVar := g.receiverName(rf.Resource)
		recvType := g.resourceGoName(rf.Resource)
		plan.recvDecl = fmt.Sprintf("(%s %s) ", recvVar, recvType)
		plan.recvExpr = fmt.Sprintf("int32(%s)", recvVar)
		baseName := g.goMethodName(rf)
		baseWrapperName := g.goFuncName(rf)
		methodSuffix := ""
		if strings.HasSuffix(wrapperName, "Async") && !strings.HasSuffix(baseWrapperName, "Async") {
			methodSuffix = "Async"
		}
		plan.methodName = baseName + methodSuffix
		if len(wrapperParams) > 0 {
			plan.methodParams = wrapperParams[1:]
		}
		plan.startIdx = 1
	} else {
		plan.methodParams = wrapperParams
	}

	// Determine return type, return mode.
	if rf.Result != nil {
		rt := g.deref(rf.Result)
		switch {
		case rt.Kind == KindResult:
			plan.returnType = g.resultStructName(rt)
			plan.returnMode = rmResult
		case rt.Kind == KindList:
			g.needsWasiImport = true
			g.needsUnsafe = true
			plan.returnType = g.computeListReturnType(rt, plan.goFuncName)
			plan.returnMode = rmList
			plan.retBufSize = 8
		case rt.Kind == KindOption:
			g.needsWasiImport = true
			g.needsUnsafe = true
			plan.returnType = g.goSafeType(rf.Result)
			plan.returnMode = rmOption
			plan.retBufSize, _ = g.abiSize(rt)
		case rt.Kind == KindRecord && rt.Name != "":
			g.needsUnsafe = true
			plan.returnType = g.goType(rf.Result)
			plan.returnMode = rmRecord
			retSize, retAlign := g.abiSize(rt)
			plan.retBufSize = alignTo(retSize, retAlign)
		case rt.Kind == KindTuple && g.tupleHasStreamFuture(rt):
			g.needsWasiImport = true
			g.needsUnsafe = true
			plan.returnType = g.computeTupleStreamReturnType(rt, plan.goFuncName, sfInfo)
			plan.returnMode = rmTupleStream
			retSize, retAlign := g.abiSize(rt)
			plan.retBufSize = alignTo(retSize, retAlign)
		case rt.Kind == KindFuture:
			g.needsWasiImport = true
			plan.returnType = g.computeFutureReturnType(rt, plan.goFuncName, sfInfo)
			plan.returnMode = rmFuture
		}
	}

	// For methods with no special return type, handle simple flat returns.
	if plan.returnType == "" && !plan.methodFunc {
		return nil
	}
	if plan.returnType == "" && plan.methodFunc {
		if rf.Result != nil {
			flatResults := g.flatten(rf.Result)
			if len(flatResults) == 1 {
				plan.returnType = g.goType(rf.Result)
			}
		}
	}

	// Methods with useRetptr but no return mode: no wrapper needed.
	if useRetptr && plan.methodFunc && plan.returnMode == rmNone {
		return nil
	}

	plan.asyncResultPtr = rf.IsAsync && plan.returnMode == rmResult && !blocking
	if plan.asyncResultPtr {
		plan.returnType = "(int32, *" + plan.returnType + ")"
	}

	if plan.returnMode == rmResult {
		rt := g.deref(rf.Result)
		plan.structName = g.resultStructName(rt)
		plan.sizeName = plan.structName + "Size"
	}

	// Collect keepalive params.
	for i := plan.startIdx; i < len(rf.Params); i++ {
		p := rf.Params[i]
		if g.isPrim(p.Type, "string") || g.deref(p.Type).Kind == KindList {
			plan.keepAlives = append(plan.keepAlives, witToGoParamName(p.Name))
		}
	}

	// Check for variant param in flat mode.
	if !useParamsPtr {
		for i := plan.startIdx; i < len(rf.Params); i++ {
			p := rf.Params[i]
			rt := g.deref(p.Type)
			if rt.Kind == KindVariant && len(g.flatten(p.Type)) > 1 {
				plan.hasVariantParam = true
				break
			}
		}
	}

	return plan
}

// generateFuncWrapper generates a public Go wrapper around a raw wasmimport function.
func (g *Generator) generateFuncWrapper(w *strings.Builder, rf ResolvedFunc, wrapperName, rawName string, wrapperParams []string, useParamsPtr bool, useRetptr bool, blocking bool, sfInfo []streamFutureInfo) {
	plan := g.planWrapper(rf, wrapperName, rawName, wrapperParams, useParamsPtr, useRetptr, blocking, sfInfo)
	if plan == nil {
		return
	}

	// Emit function signature.
	if plan.returnType != "" {
		fmt.Fprintf(w, "func %s%s(%s) %s {\n", plan.recvDecl, plan.methodName, strings.Join(plan.methodParams, ", "), plan.returnType)
	} else {
		fmt.Fprintf(w, "func %s%s(%s) {\n", plan.recvDecl, plan.methodName, strings.Join(plan.methodParams, ", "))
	}

	// Emit param preparation + call.
	switch {
	case plan.returnMode == rmFuture:
		// Future mode: capture flat i32 return, wrap in FutureReader.
		if plan.useParamsPtr {
			g.generateTypedParamsPacking(w, plan.rf, plan.methodParams, plan.methodFunc, plan.recvExpr)
			fmt.Fprintf(w, "\thandle := %s(unsafe.Pointer(&params))\n", plan.rawName)
		} else {
			callArgs := g.buildFlatCallArgs(w, plan, "\t")
			fmt.Fprintf(w, "\thandle := %s(%s)\n", plan.rawName, strings.Join(callArgs, ", "))
		}
		g.emitKeepAlives(w, plan.keepAlives, "\t")
		fmt.Fprintf(w, "\treturn %s{Handle: handle}\n", plan.returnType)
	case plan.useParamsPtr:
		g.generateTypedParamsPacking(w, plan.rf, plan.methodParams, plan.methodFunc, plan.recvExpr)
		callArgs := append([]string{"unsafe.Pointer(&params)"}, g.buildRetptrArg(w, plan)...)
		g.emitCall(w, plan.rawName, callArgs, plan, "\t", false)
	case plan.hasVariantParam:
		g.generateFlatModeVariantCall(w, plan)
	default:
		callArgs := append(g.buildFlatCallArgs(w, plan, "\t"), g.buildRetptrArg(w, plan)...)
		g.emitCall(w, plan.rawName, callArgs, plan, "\t", false)
	}

	// Emit return decode for non-result return modes.
	switch plan.returnMode {
	case rmList:
		g.emitListReturn(w, g.deref(plan.rf.Result), plan.goFuncName)
	case rmOption:
		g.emitOptionReturn(w, g.deref(plan.rf.Result))
	case rmRecord:
		fmt.Fprintf(w, "\tr := buf\n")
		g.generateRecordFieldsReturn(w, g.deref(plan.rf.Result), g.goType(plan.rf.Result), 0)
	case rmTupleStream:
		g.emitTupleStreamReturn(w, plan.rf, g.deref(plan.rf.Result), plan.sfInfo, plan.goFuncName)
	}

	fmt.Fprintf(w, "}\n\n")
}

// buildFlatCallArgs decomposes typed params into flat call arguments.
func (g *Generator) buildFlatCallArgs(w *strings.Builder, plan *wrapperPlan, indent string) []string {
	var callArgs []string
	if plan.methodFunc {
		callArgs = append(callArgs, plan.recvExpr)
	}
	for i := plan.startIdx; i < len(plan.rf.Params); i++ {
		p := plan.rf.Params[i]
		paramName := witToGoParamName(p.Name)
		callArgs = append(callArgs, g.emitDecomposeParam(w, paramName, p.Type, indent)...)
	}
	return callArgs
}

// buildRetptrArg emits retptr buffer declarations and returns the retptr call arg(s).
func (g *Generator) buildRetptrArg(w *strings.Builder, plan *wrapperPlan) []string {
	switch {
	case plan.asyncResultPtr:
		fmt.Fprintf(w, "\tretptr := wasi.AllocResult(%s)\n", plan.sizeName)
		return []string{"retptr"}
	case plan.returnMode == rmResult:
		fmt.Fprintf(w, "\tvar result %s\n", plan.structName)
		return []string{"unsafe.Pointer(&result)"}
	case plan.returnMode != rmNone:
		emitAlignedBuf(w, "buf", plan.retBufSize)
		return []string{"unsafe.Pointer(&buf)"}
	}
	return nil
}

// generateTypedParamsPacking packs typed wrapper params into a params_ptr buffer.
// Unlike generateParamsPacking (which takes flat param names), this handles
// typed Go values like strings, lists, variants, and option<variant>.
func (g *Generator) generateTypedParamsPacking(w *strings.Builder, rf ResolvedFunc, methodParams []string, methodFunc bool, recvExpr string) {
	totalSize := g.paramsTupleSize(rf)
	emitAlignedBuf(w, "params", totalSize)

	offset := 0
	pi := 0
	for i, p := range rf.Params {
		ps, pa := g.abiSize(p.Type)
		offset = alignTo(offset, pa)

		if methodFunc && i == 0 {
			fmt.Fprintf(w, "\t*(*int32)(unsafe.Pointer(&params.b[%d])) = %s\n", offset, recvExpr)
			offset += ps
			continue
		}

		paramName := strings.SplitN(methodParams[pi], " ", 2)[0]
		rt := g.deref(p.Type)
		switch rt.Kind {
		case KindPrimitive:
			if rt.Primitive == "string" {
				g.needsWasiImport = true
				fmt.Fprintf(w, "\t%sPtr, %sLen := wasi.StringToPtr(%s)\n", paramName, paramName, paramName)
				fmt.Fprintf(w, "\t*(*int32)(unsafe.Pointer(&params.b[%d])) = %sPtr\n", offset, paramName)
				fmt.Fprintf(w, "\t*(*int32)(unsafe.Pointer(&params.b[%d])) = %sLen\n", offset+4, paramName)
				pi++
			} else {
				g.emitStoreValue(w, p.Type, "params.b", offset, paramName)
				pi++
			}
		case KindVariant:
			g.emitStoreValue(w, p.Type, "params.b", offset, paramName)
			pi++
		case KindList:
			// Decompose list into ptr/len.
			g.needsWasiImport = true
			fmt.Fprintf(w, "\tif len(%s) > 0 {\n", paramName)
			fmt.Fprintf(w, "\t\t*(*int32)(unsafe.Pointer(&params.b[%d])) = int32(uintptr(unsafe.Pointer(unsafe.SliceData(%s))))\n", offset, paramName)
			fmt.Fprintf(w, "\t}\n")
			fmt.Fprintf(w, "\t*(*int32)(unsafe.Pointer(&params.b[%d])) = int32(len(%s))\n", offset+4, paramName)
			pi++
		case KindStream:
			// Stream param is typed StreamReader; extract .Handle.
			fmt.Fprintf(w, "\t*(*int32)(unsafe.Pointer(&params.b[%d])) = %s.Handle\n", offset, paramName)
			pi++
		case KindOption:
			if rt.Inner != nil {
				innerRT := g.deref(rt.Inner)
				if innerRT.Kind == KindVariant {
					// option<variant>: param is *VariantType.
					innerGoType := g.goType(rt.Inner)
					_, innerAlign := g.abiSize(rt.Inner)
					payloadOff := alignTo(1, innerAlign)
					fmt.Fprintf(w, "\tif %s != nil {\n", paramName)
					fmt.Fprintf(w, "\t\tparams.b[%d] = 1\n", offset) // some
					fmt.Fprintf(w, "\t\t*(*%s)(unsafe.Pointer(&params.b[%d])) = *%s\n", innerGoType, offset+payloadOff, paramName)
					fmt.Fprintf(w, "\t}\n")
					pi++
				} else {
					// Non-variant option: flat params.
					flat := g.flatten(p.Type)
					flatNames := make([]string, len(flat))
					for j := range flat {
						flatNames[j] = strings.SplitN(methodParams[pi], " ", 2)[0]
						pi++
					}
					g.generateTypeStore(w, p.Type, offset, flatNames, 0)
				}
			} else {
				fmt.Fprintf(w, "\tparams.b[%d] = byte(%s)\n", offset, paramName)
				pi++
			}
		default:
			// Other types: use existing flat packing.
			flat := g.flatten(p.Type)
			if len(flat) == 1 {
				g.emitStoreValue(w, p.Type, "params.b", offset, paramName)
				pi++
			} else {
				flatNames := make([]string, len(flat))
				for j := range flat {
					flatNames[j] = strings.SplitN(methodParams[pi], " ", 2)[0]
					pi++
				}
				g.generateTypeStore(w, p.Type, offset, flatNames, 0)
			}
		}

		offset += ps
	}
}

// emitCall emits the call + post-call handling (async wait, keepalive, return).
// When deferResult is true, result-return and keepalive are deferred to the caller
// (used by variant dispatch where these happen after the switch).
func (g *Generator) emitCall(w *strings.Builder, rawName string, args []string, plan *wrapperPlan, indent string, deferResult bool) {
	callExpr := fmt.Sprintf("%s(%s)", rawName, strings.Join(args, ", "))

	// Step 1: Emit call. Async calls always capture status.
	// asyncResultPtr always returns early with (status, *Result).
	if plan.asyncResultPtr {
		fmt.Fprintf(w, "%sstatus := %s\n", indent, callExpr)
		g.emitKeepAlives(w, plan.keepAlives, indent)
		fmt.Fprintf(w, "%sreturn status, (*%s)(retptr)\n", indent, plan.structName)
		return
	}

	needsStatus := plan.isAsync
	returnType := ""
	if plan.returnMode == rmNone || plan.returnMode == rmResult {
		returnType = plan.returnType
	}

	// Non-async with simple return type and no post-call work: return directly.
	if !needsStatus && plan.returnMode != rmResult && !deferResult {
		switch {
		case returnType == "bool":
			fmt.Fprintf(w, "%sreturn %s != 0\n", indent, callExpr)
		case returnType != "":
			fmt.Fprintf(w, "%sreturn %s(%s)\n", indent, returnType, callExpr)
		default:
			fmt.Fprintf(w, "%s%s\n", indent, callExpr)
		}
		g.emitKeepAlives(w, plan.keepAlives, indent)
		return
	}

	// Emit call, capturing status if async.
	if needsStatus {
		fmt.Fprintf(w, "%sstatus := %s\n", indent, callExpr)
	} else {
		fmt.Fprintf(w, "%s%s\n", indent, callExpr)
	}

	// Step 2: Async wait for blocking wrappers.
	if plan.isAsync && plan.blocking {
		fmt.Fprintf(w, "%swasi.AsyncWait(status)\n", indent)
	}

	if deferResult {
		return
	}

	// Step 3: Keepalive.
	g.emitKeepAlives(w, plan.keepAlives, indent)

	// Step 4: Result handling.
	switch {
	case plan.returnMode == rmResult:
		fmt.Fprintf(w, "%sreturn result\n", indent)
	case plan.isAsync && !plan.blocking:
		fmt.Fprintf(w, "%sreturn status\n", indent)
	}
}

// emitKeepAlives emits wasi.KeepAlive calls for string and list params.
func (g *Generator) emitKeepAlives(w *strings.Builder, params []string, indent string) {
	for _, p := range params {
		g.needsWasiImport = true
		fmt.Fprintf(w, "%swasi.KeepAlive(%s)\n", indent, p)
	}
}

// emitDecomposeParam decomposes a string, list, or stream param into flat call args.
func (g *Generator) emitDecomposeParam(w *strings.Builder, paramName string, rt *ResolvedType, indent string) []string {
	rt = g.deref(rt)
	if rt.Kind == KindPrimitive && rt.Primitive == "string" {
		g.needsWasiImport = true
		fmt.Fprintf(w, "%s%sPtr, %sLen := wasi.StringToPtr(%s)\n", indent, paramName, paramName, paramName)
		return []string{paramName + "Ptr", paramName + "Len"}
	}
	if rt.Kind == KindList {
		fmt.Fprintf(w, "%svar %sPtr int32\n", indent, paramName)
		fmt.Fprintf(w, "%sif len(%s) > 0 {\n", indent, paramName)
		fmt.Fprintf(w, "%s\t%sPtr = int32(uintptr(unsafe.Pointer(unsafe.SliceData(%s))))\n", indent, paramName, paramName)
		fmt.Fprintf(w, "%s}\n", indent)
		return []string{paramName + "Ptr", fmt.Sprintf("int32(len(%s))", paramName)}
	}
	if rt.Kind == KindStream {
		return []string{paramName + ".Handle"}
	}
	return []string{paramName}
}

// computeListReturnType computes the Go return type for a list<T>.
func (g *Generator) computeListReturnType(rt *ResolvedType, goFuncName string) string {
	if rt.Inner != nil {
		innerRT := g.deref(rt.Inner)
		if innerRT.Kind == KindPrimitive {
			if innerRT.Primitive == "u8" {
				return "[]byte"
			}
			if innerRT.Primitive == "string" {
				return "[]string"
			}
		}
		if innerRT.Kind == KindTuple || innerRT.Kind == KindRecord {
			return "[]" + goFuncName + "Entry"
		}
		return "[]" + g.goType(rt.Inner)
	}
	return "[]byte"
}

// futureResultGoType returns the Go type for a future's result payload.
func (g *Generator) futureResultGoType(rt *ResolvedType) string {
	if rt.Inner != nil {
		innerRT := g.deref(rt.Inner)
		if innerRT.Kind == KindResult {
			return g.resultStructName(innerRT)
		}
		return g.goType(rt.Inner)
	}
	return "struct{}"
}

// resultSFParamCount returns the number of stream/future type params in the
// parameter list, by subtracting the result-side count from the total.
func (g *Generator) resultSFParamCount(rt *ResolvedType, sfInfo []streamFutureInfo) int {
	var resultSF []streamFutureInfo
	g.collectStreamFuture(rt, &resultSF)
	return len(sfInfo) - len(resultSF)
}

// computeTupleStreamReturnType computes the Go return type for a tuple
// containing stream and/or future fields.
func (g *Generator) computeTupleStreamReturnType(rt *ResolvedType, goFuncName string, sfInfo []streamFutureInfo) string {
	paramSFCount := g.resultSFParamCount(rt, sfInfo)
	var returnTypes []string
	sfIdx := 0
	for _, f := range rt.Fields {
		ft := g.deref(f.Type)
		switch ft.Kind {
		case KindStream:
			opsName := streamOpsName(goFuncName, sfInfo[paramSFCount+sfIdx].typeParamIdx)
			returnTypes = append(returnTypes, fmt.Sprintf("wasi.StreamReader[%s, %s]", opsName, g.innerGoType(ft)))
			sfIdx++
		case KindFuture:
			opsName := futureOpsName(goFuncName, sfInfo[paramSFCount+sfIdx].typeParamIdx)
			returnTypes = append(returnTypes, fmt.Sprintf("wasi.FutureReader[%s, %s]", opsName, g.futureResultGoType(ft)))
			sfIdx++
		default:
			returnTypes = append(returnTypes, g.goType(f.Type))
		}
	}
	return "(" + strings.Join(returnTypes, ", ") + ")"
}

// computeFutureReturnType computes the Go return type for a future<T>.
func (g *Generator) computeFutureReturnType(rt *ResolvedType, goFuncName string, sfInfo []streamFutureInfo) string {
	paramSFCount := g.resultSFParamCount(rt, sfInfo)
	opsName := futureOpsName(goFuncName, sfInfo[paramSFCount].typeParamIdx)
	return fmt.Sprintf("wasi.FutureReader[%s, %s]", opsName, g.futureResultGoType(rt))
}

// emitListReturn emits the list return decoding logic after the raw call.
// Reads (ptr, len) from buf, decodes elements, frees list memory.
func (g *Generator) emitListReturn(w *strings.Builder, rt *ResolvedType, goFuncName string) {
	fmt.Fprintf(w, "\tlistPtr := *(*int32)(unsafe.Pointer(&buf.b[0]))\n")
	fmt.Fprintf(w, "\tlistLen := *(*int32)(unsafe.Pointer(&buf.b[4]))\n")
	fmt.Fprintf(w, "\tif listLen == 0 {\n\t\treturn nil\n\t}\n")

	if g.isPrim(rt.Inner, "u8") {
		// list<u8> → []byte: copy bytes
		fmt.Fprintf(w, "\tresult := make([]byte, listLen)\n")
		fmt.Fprintf(w, "\tcopy(result, unsafe.Slice((*byte)(unsafe.Pointer(uintptr(listPtr))), listLen))\n")
	} else if g.isPrim(rt.Inner, "string") {
		// list<string> → []string: copy each string
		fmt.Fprintf(w, "\tresult := make([]string, listLen)\n")
		fmt.Fprintf(w, "\tdata := unsafe.Pointer(uintptr(listPtr))\n")
		fmt.Fprintf(w, "\tfor i := int32(0); i < listLen; i++ {\n")
		fmt.Fprintf(w, "\t\tbase := unsafe.Add(data, uintptr(i)*8)\n")
		fmt.Fprintf(w, "\t\tresult[i] = wasi.CopyString(*(*int32)(base), *(*int32)(unsafe.Add(base, 4)))\n")
		fmt.Fprintf(w, "\t}\n")
	} else if rt.Inner != nil {
		innerRT := g.deref(rt.Inner)
		if innerRT.Kind == KindTuple || innerRT.Kind == KindRecord {
			entryTypeName := goFuncName + "Entry"
			elemSize, _ := g.abiSize(innerRT)
			fmt.Fprintf(w, "\tresult := make([]%s, listLen)\n", entryTypeName)
			fmt.Fprintf(w, "\tdata := unsafe.Pointer(uintptr(listPtr))\n")
			fmt.Fprintf(w, "\tfor i := int32(0); i < listLen; i++ {\n")
			fmt.Fprintf(w, "\t\tbase := unsafe.Add(data, uintptr(i)*%d)\n", elemSize)
			fmt.Fprintf(w, "\t\tresult[i] = %s{\n", entryTypeName)
			g.emitRecordFieldsInline(w, innerRT, BufBase(), 0, "\t\t\t")
			fmt.Fprintf(w, "\t\t}\n")
			fmt.Fprintf(w, "\t}\n")
		} else {
			// Generic list — decode as primitive elements.
			elemGoType := g.goType(rt.Inner)
			elemSize, _ := g.abiSize(rt.Inner)
			fmt.Fprintf(w, "\tresult := make([]%s, listLen)\n", elemGoType)
			fmt.Fprintf(w, "\tdata := unsafe.Pointer(uintptr(listPtr))\n")
			fmt.Fprintf(w, "\tfor i := int32(0); i < listLen; i++ {\n")
			if elemSize == 1 {
				fmt.Fprintf(w, "\t\tresult[i] = %s(*(*byte)(unsafe.Add(data, uintptr(i))))\n", elemGoType)
			} else {
				st := sizedType(elemSize)
				fmt.Fprintf(w, "\t\tresult[i] = %s(*(*%s)(unsafe.Add(data, uintptr(i)*%d)))\n", elemGoType, st, elemSize)
			}
			fmt.Fprintf(w, "\t}\n")
		}
	}

	fmt.Fprintf(w, "\twasi.FreeRealloc(listPtr)\n")
	fmt.Fprintf(w, "\treturn result\n")
}

// emitOptionReturn emits the option return decoding logic after the raw call.
func (g *Generator) emitOptionReturn(w *strings.Builder, rt *ResolvedType) {
	var payloadAlign int
	if rt.Inner != nil {
		_, payloadAlign = g.abiSize(rt.Inner)
	} else {
		payloadAlign = 1
	}
	payloadOff := alignTo(1, payloadAlign)

	// Check discriminant.
	fmt.Fprintf(w, "\tif buf.b[0] == 0 {\n\t\treturn nil\n\t}\n")

	// Decode payload based on inner type.
	ba := BufField("buf.b")
	if rt.Inner != nil {
		innerGoType := g.goType(rt.Inner)
		if g.isPrim(rt.Inner, "string") {
			fmt.Fprintf(w, "\tv := wasi.CopyString(*(*int32)(%s), *(*int32)(%s))\n",
				ba.ptrAt(payloadOff), ba.ptrAt(payloadOff+4))
			fmt.Fprintf(w, "\treturn &v\n")
		} else if expr, ok := g.emitReadExpr(rt.Inner, ba, payloadOff); ok {
			fmt.Fprintf(w, "\tv := %s\n", expr)
			fmt.Fprintf(w, "\treturn &v\n")
		} else {
			fmt.Fprintf(w, "\t// TODO: option<%s> decode\n", innerGoType)
			fmt.Fprintf(w, "\treturn nil\n")
		}
	} else {
		fmt.Fprintf(w, "\treturn nil\n")
	}
}

// emitTupleStreamReturn emits the tuple+stream/future return decoding logic.
func (g *Generator) emitTupleStreamReturn(w *strings.Builder, rf ResolvedFunc, rt *ResolvedType, sfInfo []streamFutureInfo, goFuncName string) {
	paramSFCount := g.resultSFParamCount(rt, sfInfo)
	var returnExprs []string
	sfIdx := 0
	offset := 0
	for _, f := range rt.Fields {
		fs, fa := g.abiSize(f.Type)
		offset = alignTo(offset, fa)
		ft := g.deref(f.Type)
		switch ft.Kind {
		case KindStream:
			opsName := streamOpsName(goFuncName, sfInfo[paramSFCount+sfIdx].typeParamIdx)
			returnExprs = append(returnExprs, fmt.Sprintf("wasi.StreamReader[%s, %s]{Handle: *(*int32)(unsafe.Pointer(&buf.b[%d]))}", opsName, g.innerGoType(ft), offset))
			sfIdx++
		case KindFuture:
			opsName := futureOpsName(goFuncName, sfInfo[paramSFCount+sfIdx].typeParamIdx)
			returnExprs = append(returnExprs, fmt.Sprintf("wasi.FutureReader[%s, %s]{Handle: *(*int32)(unsafe.Pointer(&buf.b[%d]))}", opsName, g.futureResultGoType(ft), offset))
			sfIdx++
		default:
			returnExprs = append(returnExprs, fmt.Sprintf("*(*int32)(unsafe.Pointer(&buf.b[%d]))", offset))
		}
		offset += fs
	}

	fmt.Fprintf(w, "\treturn %s\n", strings.Join(returnExprs, ",\n\t\t"))
}

// decomposedParamArgs returns the flat call args for a param that was already
// decomposed by emitDecomposeParam. Used in variant dispatch where params
// are decomposed before the switch and referenced per-case.
func (g *Generator) decomposedParamArgs(paramName string, rt *ResolvedType) []string {
	rt = g.deref(rt)
	switch rt.Kind {
	case KindPrimitive:
		if rt.Primitive == "string" {
			return []string{paramName + "Ptr", paramName + "Len"}
		}
	case KindList:
		return []string{paramName + "Ptr", fmt.Sprintf("int32(len(%s))", paramName)}
	case KindStream:
		return []string{paramName + ".Handle"}
	}
	return []string{paramName}
}

// generateFlatModeVariantCall emits a switch statement that dispatches on a
// variant parameter's discriminant, building per-case flat call args.
func (g *Generator) generateFlatModeVariantCall(w *strings.Builder, plan *wrapperPlan) {
	// Find the variant param.
	variantParamIdx := -1
	var variantWITParam ResolvedParam
	for i := plan.startIdx; i < len(plan.rf.Params); i++ {
		p := plan.rf.Params[i]
		rt := g.deref(p.Type)
		if rt.Kind == KindVariant && len(g.flatten(p.Type)) > 1 {
			variantParamIdx = i
			variantWITParam = p
			break
		}
	}
	if variantParamIdx < 0 {
		return
	}

	variantRT := g.deref(variantWITParam.Type)
	variantGoName := witToGoParamName(variantWITParam.Name)
	variantTypeName := g.goType(variantWITParam.Type)

	// Decompose non-variant typed params (strings, lists) first.
	for i := plan.startIdx; i < len(plan.rf.Params); i++ {
		if i == variantParamIdx {
			continue
		}
		p := plan.rf.Params[i]
		g.emitDecomposeParam(w, witToGoParamName(p.Name), p.Type, "\t")
	}

	// Result buffer and retptr arg.
	retptrArg := ""
	switch {
	case plan.asyncResultPtr:
		fmt.Fprintf(w, "\tretptr := wasi.AllocResult(%s)\n", plan.sizeName)
		retptrArg = "retptr"
	case plan.returnMode == rmResult:
		fmt.Fprintf(w, "\tvar result %s\n", plan.structName)
		retptrArg = "unsafe.Pointer(&result)"
	}

	// Generate switch on variant disc.
	flat := g.flatten(variantWITParam.Type)
	maxPayloadFlat := len(flat) - 1

	fmt.Fprintf(w, "\tswitch %s.Disc() {\n", variantGoName)
	for caseIdx, c := range variantRT.Cases {
		fmt.Fprintf(w, "\tcase %s:\n", variantTypeName+witToGoName(c.Name))

		var caseCallArgs []string
		if plan.methodFunc {
			caseCallArgs = append(caseCallArgs, plan.recvExpr)
		}

		for i := plan.startIdx; i < len(plan.rf.Params); i++ {
			p := plan.rf.Params[i]
			if i == variantParamIdx {
				// Variant: disc + flatten case payload + padding.
				caseCallArgs = append(caseCallArgs, fmt.Sprintf("%d", caseIdx))
				if c.Type != nil {
					caseName := witToGoName(c.Name)
					caseVar := strings.ToLower(caseName[:1]) + caseName[1:]
					fmt.Fprintf(w, "\t\t%s := %s.%s()\n", caseVar, variantGoName, caseName)
					caseCallArgs = append(caseCallArgs, g.flattenFieldExprs(c.Type, caseVar)...)
				}
				for j := len(g.flatten(c.Type)); j < maxPayloadFlat; j++ {
					caseCallArgs = append(caseCallArgs, "0")
				}
			} else {
				caseCallArgs = append(caseCallArgs, g.decomposedParamArgs(witToGoParamName(p.Name), p.Type)...)
			}
		}

		if retptrArg != "" {
			caseCallArgs = append(caseCallArgs, retptrArg)
		}

		g.emitCall(w, plan.rawName, caseCallArgs, plan, "\t\t", true)
	}
	fmt.Fprintf(w, "\tdefault:\n")
	fmt.Fprintf(w, "\t\tpanic(\"%s: invalid discriminant\")\n", variantTypeName)
	fmt.Fprintf(w, "\t}\n")

	g.emitKeepAlives(w, plan.keepAlives, "\t")

	if plan.returnMode == rmResult && !plan.asyncResultPtr {
		fmt.Fprintf(w, "\treturn result\n")
	}
}

// --- Variant flattening helpers ---

// flattenPrimExpr returns the flat expression(s) for a WIT primitive value.
func flattenPrimExpr(prim, expr string) []string {
	switch prim {
	case "f32", "f64":
		return []string{expr}
	case "u64", "s64":
		return []string{fmt.Sprintf("int64(%s)", expr)}
	}
	return []string{fmt.Sprintf("int32(%s)", expr)}
}

// flattenFieldExprs flattens a typed Go expression into ABI flat values.
func (g *Generator) flattenFieldExprs(rt *ResolvedType, expr string) []string {
	rt = g.deref(rt)
	switch rt.Kind {
	case KindPrimitive:
		return flattenPrimExpr(rt.Primitive, expr)
	case KindTypeAlias:
		if rt.Alias != nil {
			return g.flattenFieldExprs(rt.Alias, expr)
		}
		return flattenPrimExpr(rt.Primitive, expr)
	case KindRecord, KindTuple:
		var exprs []string
		for _, f := range rt.Fields {
			fieldExpr := expr + "." + witToGoName(f.Name)
			exprs = append(exprs, g.flattenFieldExprs(f.Type, fieldExpr)...)
		}
		return exprs
	case KindEnum, KindFlags, KindHandle, KindResource:
		return []string{fmt.Sprintf("int32(%s)", expr)}
	}
	return []string{fmt.Sprintf("int32(%s)", expr)}
}
