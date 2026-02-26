// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package posix

import (
	wasi "internal/wasi"
	"internal/wasi/generated/sockets"
)

// Type aliases for typed stream/future handles used within the posix package.
type (
	TCPRecvReader   = wasi.StreamReader[sockets.TCPSocketReceiveStreamOps0, byte]
	TCPRecvFuture   = wasi.FutureReader[sockets.TCPSocketReceiveFutureOps1, sockets.ResultVoidErrorCode]
	TCPSendWriter   = wasi.StreamWriter[sockets.TCPSocketSendStreamOps0, byte]
	TCPSendFuture   = wasi.FutureReader[sockets.TCPSocketSendFutureOps1, sockets.ResultVoidErrorCode]
	TCPSendReader   = wasi.StreamReader[sockets.TCPSocketSendStreamOps0, byte]
	TCPAcceptReader = wasi.StreamReader[sockets.TCPSocketListenStreamOps0, sockets.TCPSocket]
)

// NewTCPSendStreamPair creates a new send stream reader/writer pair
// for TCP sockets. The reader should be passed to TCPSocket.Send(),
// and the writer is used to write outgoing data.
func NewTCPSendStreamPair() (TCPSendReader, TCPSendWriter) {
	return wasi.NewStreamPair[sockets.TCPSocketSendStreamOps0, byte]()
}

// NewTCPAcceptStreamReader wraps a raw accept stream handle into a typed
// StreamReader[TCPSocket] with the correct ops type for stream<tcp-socket>.
func NewTCPAcceptStreamReader(handle int32) TCPAcceptReader {
	return TCPAcceptReader{Handle: handle}
}
