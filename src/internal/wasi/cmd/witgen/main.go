// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// witgen generates Go bindings from WIT (WebAssembly Interface Types)
// definitions. It reads the JSON output of `wasm-tools component wit --json`
// and produces Go source files with //go:wasmimport declarations and
// canonical ABI struct layouts.
//
// Usage:
//
//	wasm-tools component wit ./wit --json | witgen -pkg clocks -tags wasip3 \
//	  -outdir src/internal/wasi/clocks \
//	  -gen 'wasi:clocks@0.3.0-rc-2026-02-09/types:types.go' \
//	  -gen 'wasi:clocks@0.3.0-rc-2026-02-09/monotonic-clock:clocks.go:Monotonic'
//
// This is a prototype tailored for the Go standard library's needs,
// producing slim bindings without a heavyweight runtime library.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// repeatFlag implements flag.Value for repeatable flags.
type repeatFlag []string

func (r *repeatFlag) String() string { return strings.Join(*r, ",") }
func (r *repeatFlag) Set(s string) error {
	*r = append(*r, s)
	return nil
}

func main() {
	pkgFlag := flag.String("pkg", "bindings", "Go package name for generated code")
	buildTagFlag := flag.String("tags", "", "build constraint (e.g., wasip3)")
	outDirFlag := flag.String("outdir", "", "output directory for generated files")
	var gens repeatFlag
	flag.Var(&gens, "gen", "generation spec: 'iface:file[:prefix]' (repeatable)")
	var imports repeatFlag
	flag.Var(&imports, "import", "import mapping: 'wit-pkg:go-import-path:alias' (repeatable)")
	flag.Parse()

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading stdin: %v\n", err)
		os.Exit(1)
	}

	var doc WITDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing JSON: %v\n", err)
		os.Exit(1)
	}

	if len(gens) == 0 {
		// List available interfaces.
		fmt.Fprintf(os.Stderr, "Available interfaces:\n")
		for i, iface := range doc.Interfaces {
			pkg := doc.Packages[iface.Package]
			fmt.Fprintf(os.Stderr, "  [%d] %s/%s\n", i, pkg.Name, iface.Name)
		}
		os.Exit(1)
	}

	if *outDirFlag == "" {
		fmt.Fprintf(os.Stderr, "error: -outdir is required\n")
		os.Exit(1)
	}

	// Parse -gen specs. Interface names contain a colon (e.g.,
	// "wasi:clocks@0.3.0/types"), so we split from the right to find
	// the file and optional prefix fields.
	var specs []GenSpec
	for _, g := range gens {
		// Find last colon — could be prefix or filename separator.
		lastColon := strings.LastIndex(g, ":")
		if lastColon < 0 {
			fmt.Fprintf(os.Stderr, "invalid -gen spec %q: expected 'iface:file[:prefix]'\n", g)
			os.Exit(1)
		}
		// Check if there's a second-to-last colon that separates iface from file.
		secondColon := strings.LastIndex(g[:lastColon], ":")
		var spec GenSpec
		if secondColon < 0 {
			// Only one colon — can't be valid since iface itself has a colon.
			fmt.Fprintf(os.Stderr, "invalid -gen spec %q: expected 'iface:file[:prefix]'\n", g)
			os.Exit(1)
		}
		// Check if the part after secondColon looks like a filename (.go suffix).
		mid := g[secondColon+1 : lastColon]
		if strings.HasSuffix(mid, ".go") {
			// iface:file:prefix
			spec = GenSpec{
				IfaceName: g[:secondColon],
				Filename:  mid,
				Prefix:    g[lastColon+1:],
			}
		} else {
			// iface:file (no prefix) — lastColon separates iface from file
			spec = GenSpec{
				IfaceName: g[:lastColon],
				Filename:  g[lastColon+1:],
			}
		}
		specs = append(specs, spec)
	}

	// Parse -import specs: 'wit-pkg:go-import-path:alias'
	// WIT package names contain a colon (e.g., "wasi:clocks@0.3.0"),
	// so we split from the right: alias is last, go-import-path is
	// second-to-last, and everything before is the WIT package name.
	var importSpecs []ImportSpec
	for _, imp := range imports {
		lastColon := strings.LastIndex(imp, ":")
		if lastColon < 0 {
			fmt.Fprintf(os.Stderr, "invalid -import spec %q: expected 'wit-pkg:go-import-path:alias'\n", imp)
			os.Exit(1)
		}
		secondColon := strings.LastIndex(imp[:lastColon], ":")
		if secondColon < 0 {
			fmt.Fprintf(os.Stderr, "invalid -import spec %q: expected 'wit-pkg:go-import-path:alias'\n", imp)
			os.Exit(1)
		}
		importSpecs = append(importSpecs, ImportSpec{
			WITPkg:   imp[:secondColon],
			GoImport: imp[secondColon+1 : lastColon],
			GoAlias:  imp[lastColon+1:],
		})
	}

	g := &Generator{
		doc:         &doc,
		pkgName:     *pkgFlag,
		buildTag:    *buildTagFlag,
		outDir:      *outDirFlag,
		specs:       specs,
		importSpecs: importSpecs,
	}

	if err := g.resolveTypes(); err != nil {
		fmt.Fprintf(os.Stderr, "error resolving types: %v\n", err)
		os.Exit(1)
	}

	// Resolve interface indices for all specs.
	for i := range g.specs {
		g.specs[i].IfaceIdx = -1
		for j, iface := range doc.Interfaces {
			pkg := doc.Packages[iface.Package]
			fullName := fmt.Sprintf("%s/%s", pkg.Name, iface.Name)
			if fullName == g.specs[i].IfaceName {
				g.specs[i].IfaceIdx = j
				break
			}
		}
		if g.specs[i].IfaceIdx < 0 {
			fmt.Fprintf(os.Stderr, "interface %q not found\n", g.specs[i].IfaceName)
			os.Exit(1)
		}
	}

	if err := g.generateAll(); err != nil {
		fmt.Fprintf(os.Stderr, "error generating: %v\n", err)
		os.Exit(1)
	}
}
