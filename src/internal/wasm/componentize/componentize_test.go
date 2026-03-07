// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package componentize

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

func helloWasmPath() string {
	// Try relative path first (works from repo root), then absolute fallback
	candidates := []string{
		"../../../../wasip3-examples/out/hello.wasm",
	}
	// Also check GOROOT-based path
	if goroot := os.Getenv("GOROOT"); goroot != "" {
		candidates = append(candidates, goroot+"/wasip3-examples/out/hello.wasm")
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return candidates[0] // return first for error message
}

func TestEmbed(t *testing.T) {
	coreModule, err := os.ReadFile(helloWasmPath())
	if err != nil {
		t.Skipf("skipping: %v (run wasip3-examples/build-and-test.sh first)", err)
	}

	embedded := Embed(coreModule)

	// The embedded module should be larger than the original
	if len(embedded) <= len(coreModule) {
		t.Fatalf("embedded module (%d bytes) should be larger than core (%d bytes)", len(embedded), len(coreModule))
	}

	// Should still start with wasm magic
	if !bytes.Equal(embedded[:4], []byte{0x00, 0x61, 0x73, 0x6d}) {
		t.Fatal("embedded module does not start with wasm magic")
	}

	// Verify the component-type section is present
	if !bytes.Contains(embedded, []byte("component-type")) {
		t.Fatal("embedded module does not contain component-type custom section")
	}
}

func TestNew(t *testing.T) {
	coreModule, err := os.ReadFile(helloWasmPath())
	if err != nil {
		t.Skipf("skipping: %v (run wasip3-examples/build-and-test.sh first)", err)
	}

	embedded := Embed(coreModule)
	component, err := New(embedded)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Should start with component magic
	componentMagic := []byte{0x00, 0x61, 0x73, 0x6d, 0x0d, 0x00, 0x01, 0x00}
	if !bytes.Equal(component[:8], componentMagic) {
		t.Fatalf("output does not start with component magic: got %x", component[:8])
	}

	t.Logf("component size: %d bytes", len(component))

	// Try to validate with wasm-tools if available
	wasmTools, err := exec.LookPath("wasm-tools")
	if err != nil {
		t.Log("wasm-tools not found, skipping validation")
		return
	}

	// Write component to temp file
	tmpFile, err := os.CreateTemp("", "hello-component-*.wasm")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.Write(component); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	// Validate
	cmd := exec.Command(wasmTools, "validate", "--features", "cm-async,cm-async-builtins,cm-error-context", tmpFile.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Also dump the WAT for debugging
		cmd2 := exec.Command(wasmTools, "print", tmpFile.Name())
		wat, _ := cmd2.Output()
		if len(wat) > 5000 {
			// Show first and last parts
			t.Logf("WAT (first 2500 chars):\n%s", string(wat[:2500]))
			t.Logf("WAT (last 2500 chars):\n%s", string(wat[len(wat)-2500:]))
		} else {
			t.Logf("WAT:\n%s", string(wat))
		}
		t.Fatalf("wasm-tools validate failed: %s\n%s", err, out)
	}
	t.Log("wasm-tools validate passed!")

	// Print the component WAT for debugging/comparison
	cmd = exec.Command(wasmTools, "print", tmpFile.Name())
	wat, err := cmd.Output()
	if err == nil {
		// Show just the component wrapper (skip core module internals)
		watStr := string(wat)
		// Find the part after the last core module
		lines := bytes.Split(wat, []byte("\n"))
		var componentLines [][]byte
		inCoreModule := false
		coreModuleDepth := 0
		for _, line := range lines {
			trimmed := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmed, []byte("(core module")) {
				inCoreModule = true
				coreModuleDepth++
				componentLines = append(componentLines, line)
				continue
			}
			if inCoreModule {
				if bytes.Equal(trimmed, []byte(")")) && coreModuleDepth > 0 {
					coreModuleDepth--
					if coreModuleDepth == 0 {
						inCoreModule = false
						componentLines = append(componentLines, []byte("    ;; ... core module body ..."))
						componentLines = append(componentLines, line)
					}
				}
				continue
			}
			componentLines = append(componentLines, line)
		}
		_ = watStr
		t.Logf("Component structure:\n%s", bytes.Join(componentLines, []byte("\n")))
	}
}

func TestParseWit(t *testing.T) {
	witDir := "../../wasi/wit"
	packages, err := ParseWitDirectory(witDir)
	if err != nil {
		t.Fatalf("ParseWitDirectory failed: %v", err)
	}

	t.Logf("Parsed %d packages", len(packages))
	for _, pkg := range packages {
		t.Logf("  %s:%s@%s (%d interfaces, %d worlds)",
			pkg.Namespace, pkg.Name, pkg.Version,
			len(pkg.Interfaces), len(pkg.Worlds))
		for _, iface := range pkg.Interfaces {
			t.Logf("    interface %s: %d types, %d funcs, %d uses",
				iface.Name, len(iface.TypeDefs), len(iface.Functions), len(iface.Uses))
		}
	}

	// Verify we found key interfaces
	found := make(map[string]bool)
	for _, pkg := range packages {
		for _, iface := range pkg.Interfaces {
			key := pkg.Namespace + ":" + pkg.Name + "/" + iface.Name
			found[key] = true
		}
	}
	for _, want := range []string{
		"wasi:cli/environment",
		"wasi:cli/run",
		"wasi:clocks/monotonic-clock",
		"wasi:filesystem/types",
		"wasi:sockets/types",
		"wasi:random/random",
		"wasi:exec/exec",
	} {
		if !found[want] {
			t.Errorf("missing interface: %s", want)
		}
	}

	// Verify the "command" world exists in the go:wasip3-proto package
	foundWorld := false
	for _, pkg := range packages {
		for _, w := range pkg.Worlds {
			if w.Name == "command" && pkg.Namespace == "go" {
				foundWorld = true
				t.Logf("  world %s: %d imports, %d exports", w.Name, len(w.Imports), len(w.Exports))
			}
		}
	}
	if !foundWorld {
		t.Error("did not find 'command' world in go:wasip3-proto")
	}
}

func TestBuildWorldFromWIT(t *testing.T) {
	witDir := "../../wasi/wit"
	ifaces, world, _, err := buildWorldFromWIT(witDir, "command")
	if err != nil {
		t.Fatalf("buildWorldFromWIT failed: %v", err)
	}

	t.Logf("World 'command': %d interface imports, %d exports", len(ifaces), len(world.Exports))
	for _, iface := range ifaces {
		t.Logf("  import %s: %d items, %d aliases", iface.name, len(iface.items), len(iface.aliases))
	}

	// Verify we got all expected imports (no exec in base command world)
	ifaceNames := make(map[string]bool)
	for _, iface := range ifaces {
		ifaceNames[iface.name] = true
	}
	for _, want := range []string{
		"wasi:cli/environment@0.3.0-rc-2026-02-09",
		"wasi:cli/stdout@0.3.0-rc-2026-02-09",
		"wasi:cli/stderr@0.3.0-rc-2026-02-09",
		"wasi:clocks/monotonic-clock@0.3.0-rc-2026-02-09",
		"wasi:filesystem/types@0.3.0-rc-2026-02-09",
		"wasi:random/random@0.3.0-rc-2026-02-09",
	} {
		if !ifaceNames[want] {
			t.Errorf("missing interface import: %s", want)
		}
	}
	if ifaceNames["wasi:exec/exec@0.1.0"] {
		t.Error("command world should not include wasi:exec/exec")
	}
}

func TestBuildWorldFromWITWithExec(t *testing.T) {
	witDir := "../../wasi/wit"
	ifaces, world, _, err := buildWorldFromWIT(witDir, "command-with-exec")
	if err != nil {
		t.Fatalf("buildWorldFromWIT failed: %v", err)
	}

	t.Logf("World 'command-with-exec': %d interface imports, %d exports", len(ifaces), len(world.Exports))

	ifaceNames := make(map[string]bool)
	for _, iface := range ifaces {
		ifaceNames[iface.name] = true
	}
	for _, want := range []string{
		"wasi:cli/environment@0.3.0-rc-2026-02-09",
		"wasi:cli/stdout@0.3.0-rc-2026-02-09",
		"wasi:cli/stderr@0.3.0-rc-2026-02-09",
		"wasi:clocks/monotonic-clock@0.3.0-rc-2026-02-09",
		"wasi:filesystem/types@0.3.0-rc-2026-02-09",
		"wasi:random/random@0.3.0-rc-2026-02-09",
		"wasi:exec/exec@0.1.0",
	} {
		if !ifaceNames[want] {
			t.Errorf("missing interface import: %s", want)
		}
	}
}

func TestParseCoreModule(t *testing.T) {
	coreModule, err := os.ReadFile(helloWasmPath())
	if err != nil {
		t.Skipf("skipping: %v (run wasip3-examples/build-and-test.sh first)", err)
	}

	mod, err := ParseCoreModule(coreModule)
	if err != nil {
		t.Fatalf("ParseCoreModule failed: %v", err)
	}

	t.Logf("Types: %d", len(mod.Types))
	t.Logf("Imports: %d", len(mod.Imports))
	t.Logf("Exports: %d", len(mod.Exports))

	// Print unique import modules
	modules := make(map[string]bool)
	for _, imp := range mod.Imports {
		modules[imp.Module] = true
	}
	for m := range modules {
		t.Logf("  module: %s", m)
	}

	// Verify some known imports
	foundTaskReturn := false
	foundNow := false
	for _, imp := range mod.Imports {
		if imp.Module == "[export]wasi:cli/run@0.3.0-rc-2026-02-09" && imp.Name == "[task-return]run" {
			foundTaskReturn = true
		}
		if imp.Module == "wasi:clocks/monotonic-clock@0.3.0-rc-2026-02-09" && imp.Name == "now" {
			foundNow = true
		}
	}
	if !foundTaskReturn {
		t.Error("did not find [task-return]run import")
	}
	if !foundNow {
		t.Error("did not find monotonic-clock now import")
	}

	// Verify exports
	foundMemory := false
	foundRealloc := false
	foundAsyncLift := false
	foundCallback := false
	for _, exp := range mod.Exports {
		switch exp.Name {
		case "memory":
			foundMemory = true
		case "cabi_realloc":
			foundRealloc = true
		}
		if len(exp.Name) > 12 && exp.Name[:12] == "[async-lift]" {
			foundAsyncLift = true
		}
		if len(exp.Name) > 22 && exp.Name[:22] == "[callback][async-lift]" {
			foundCallback = true
		}
	}
	if !foundMemory {
		t.Error("did not find memory export")
	}
	if !foundRealloc {
		t.Error("did not find cabi_realloc export")
	}
	if !foundAsyncLift {
		t.Error("did not find [async-lift] export")
	}
	if !foundCallback {
		t.Error("did not find [callback][async-lift] export")
	}
}
