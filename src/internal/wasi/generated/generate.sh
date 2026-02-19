#!/bin/bash
# Copyright 2026 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Regenerate all WASI 0.3 bindings from WIT definitions.
# Run from the repo root.
#
# Prerequisites:
#   - wasm-tools (cargo install wasm-tools)
set -eo pipefail

cd "$(dirname "$0")/../../../.."
WIT=src/internal/wasi/wit
WITGEN="${WITGEN:-$(mktemp -d)/witgen}"

echo "Building witgen..."
go build -o "$WITGEN" ./src/internal/wasi/cmd/witgen/

WITJSON=$(wasm-tools component wit --json "$WIT")

echo "Generating WASI 0.3 bindings..."

echo "$WITJSON" | $WITGEN -pkg sockets -tags wasip3 -outdir src/internal/wasi/generated/sockets \
  -gen 'wasi:sockets@0.3.0-rc-2026-02-09/types:sockets.go'

echo "$WITJSON" | $WITGEN -pkg filesystem -tags wasip3 -outdir src/internal/wasi/generated/filesystem \
  -import 'wasi:clocks@0.3.0-rc-2026-02-09:internal/wasi/generated/clocks:clocks' \
  -gen 'wasi:filesystem@0.3.0-rc-2026-02-09/types:filesystem.go' \
  -gen 'wasi:filesystem@0.3.0-rc-2026-02-09/preopens:preopens.go'

echo "$WITJSON" | $WITGEN -pkg clocks -tags wasip3 -outdir src/internal/wasi/generated/clocks \
  -gen 'wasi:clocks@0.3.0-rc-2026-02-09/types:types.go' \
  -gen 'wasi:clocks@0.3.0-rc-2026-02-09/monotonic-clock:clocks.go:Monotonic' \
  -gen 'wasi:clocks@0.3.0-rc-2026-02-09/system-clock:system.go:System'

echo "$WITJSON" | $WITGEN -pkg random -tags wasip3 -outdir src/internal/wasi/generated/random \
  -gen 'wasi:random@0.3.0-rc-2026-02-09/random:random.go'

echo "$WITJSON" | $WITGEN -pkg cli -tags wasip3 -outdir src/internal/wasi/generated/cli \
  -gen 'wasi:cli@0.3.0-rc-2026-02-09/types:types.go' \
  -gen 'wasi:cli@0.3.0-rc-2026-02-09/environment:environment.go' \
  -gen 'wasi:cli@0.3.0-rc-2026-02-09/stdout:stdout.go:Stdout' \
  -gen 'wasi:cli@0.3.0-rc-2026-02-09/stderr:stderr.go:Stderr'

gofmt -w src/internal/wasi/generated/sockets/ src/internal/wasi/generated/filesystem/ \
      src/internal/wasi/generated/clocks/ src/internal/wasi/generated/random/ src/internal/wasi/generated/cli/
echo "Done."
