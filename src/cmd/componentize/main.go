// Componentize wraps a core wasm module into a wasm component.
// Usage: componentize input.wasm output.wasm
package main

import (
	"fmt"
	"internal/wasm/componentize"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: componentize input.wasm output.wasm\n")
		os.Exit(1)
	}
	input, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}
	embedded := componentize.Embed(input)
	output, err := componentize.New(embedded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "componentize: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[2], output, 0o666); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
}
