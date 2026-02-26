// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package posix

import (
	"internal/wasi"
	"internal/wasi/generated/sockets"
	"unsafe"
)

// AllocSocketFD allocates a new file descriptor for a connected socket.
// Called from the net package when a connection is established.
func AllocSocketFD(sockHandle int32, recvReader TCPRecvReader, recvFuture TCPRecvFuture, sendWriter TCPSendWriter, sendFuture TCPSendFuture, readCancel, writeCancel func(int32) int32) int {
	fd := FDAlloc()
	if fd < 0 {
		return -1
	}
	FDTable[fd].IsSock = true
	FDTable[fd].SockHandle = sockHandle
	FDTable[fd].RecvReader = recvReader
	FDTable[fd].RecvFuture = recvFuture
	FDTable[fd].SendWriter = sendWriter
	FDTable[fd].SendFuture = sendFuture
	FDTable[fd].readCancel = readCancel
	FDTable[fd].writeCancel = writeCancel
	return fd
}

// AllocListenerFD allocates a new file descriptor for a listening socket.
func AllocListenerFD(sockHandle int32, acceptReader TCPAcceptReader, readCancel func(int32) int32) int {
	fd := FDAlloc()
	if fd < 0 {
		return -1
	}
	FDTable[fd].IsSock = true
	FDTable[fd].SockHandle = sockHandle
	FDTable[fd].AcceptReader = acceptReader
	FDTable[fd].readCancel = readCancel
	return fd
}

// AllocUDPSocketFD allocates a new file descriptor for a UDP socket.
// UDP sockets don't use streams — send/receive are individual async calls.
func AllocUDPSocketFD(sockHandle int32) int {
	fd := FDAlloc()
	if fd < 0 {
		return -1
	}
	FDTable[fd].IsSock = true
	FDTable[fd].IsUDP = true
	FDTable[fd].SockHandle = sockHandle
	return fd
}

// CheckStreamDropped checks whether the host delivered a CopyDropped (or
// CopyCancelled) event for a stream handle while no goroutine was waiting.
// Returns the event result, or 0 if none pending.
//
// Provided by runtime via //go:linkname.
//
//go:linkname checkStreamDropped
func checkStreamDropped(handle int32) int32

// SockRead reads from a socket stream.
// Blocks internally via awaitStream when the CM stream read returns Blocked.
// Returns ErrBlocked only on deadline/close interrupts (maps to EAGAIN).
func SockRead(f *CMFD, b []byte) (int, error) {
	if f.RecvReader.Handle == 0 {
		return 0, nil // zero-read → poll.FD converts to io.EOF
	}
	if result := checkStreamDropped(f.RecvReader.Handle); result != 0 {
		f.RecvReader.Drop()
		f.RecvReader.Handle = 0
		return 0, nil // EOF
	}
	result := f.RecvReader.Read(b)
	if result == wasi.Blocked {
		var err error
		result, err = awaitStream(f.RecvReader.Handle, f.readCancel, f.runtimeCtx, 'r')
		if err != nil {
			return 0, err // ErrBlocked → EAGAIN → pd.waitRead → deadline error
		}
	}
	copyResult, progress := wasi.UnpackCopy(result)
	if copyResult == wasi.CopyDropped || copyResult == wasi.CopyCancelled {
		f.RecvReader.Drop()
		f.RecvReader.Handle = 0
		if progress > 0 {
			return progress, nil
		}
		return 0, nil // EOF
	}
	return progress, nil
}

// SockWrite writes to a socket stream.
// Blocks internally via awaitStream when the CM stream write returns Blocked.
// Returns ErrBlocked only on deadline/close interrupts (maps to EAGAIN).
func SockWrite(f *CMFD, b []byte) (int, error) {
	if f.SendWriter.Handle == 0 {
		return 0, ErrPipe
	}
	if result := checkStreamDropped(f.SendWriter.Handle); result != 0 {
		f.SendWriter.Drop()
		f.SendWriter.Handle = 0
		return 0, ErrPipe
	}
	result := f.SendWriter.Write(b)
	if result == wasi.Blocked {
		var err error
		result, err = awaitStream(f.SendWriter.Handle, f.writeCancel, f.runtimeCtx, 'w')
		if err != nil {
			return 0, err // ErrBlocked → EAGAIN → pd.waitWrite → deadline error
		}
	}
	copyResult, progress := wasi.UnpackCopy(result)
	if copyResult == wasi.CopyDropped || copyResult == wasi.CopyCancelled {
		f.SendWriter.Drop()
		f.SendWriter.Handle = 0
		return 0, ErrPipe
	}
	return progress, nil
}

// SockClose cleans up all socket resources.
func SockClose(f *CMFD) {
	if f.ConnectSubtask != 0 {
		SubtaskCancelAndWait(f.ConnectSubtask)
		wasi.FreeResult(f.ConnectResult)
		f.ConnectSubtask = 0
		f.ConnectResult = nil
	}
	if f.AcceptReader.Handle != 0 {
		f.AcceptReader.Drop()
		f.AcceptReader.Handle = 0
	}
	if f.RecvReader.Handle != 0 {
		f.RecvReader.Drop()
		f.RecvReader.Handle = 0
	}
	if f.SendWriter.Handle != 0 {
		f.SendWriter.Drop()
		f.SendWriter.Handle = 0
	}
	if f.SendFuture.Handle != 0 {
		f.SendFuture.Drop()
		f.SendFuture.Handle = 0
	}
	if f.RecvFuture.Handle != 0 {
		f.RecvFuture.Drop()
		f.RecvFuture.Handle = 0
	}
	if f.SockHandle != 0 {
		if f.IsUDP {
			sockets.UDPSocket(f.SockHandle).Drop()
		} else {
			sockets.TCPSocket(f.SockHandle).Drop()
		}
		f.SockHandle = 0
	}
}

// SockCloseRead closes the read direction of a socket.
func SockCloseRead(fd int) error {
	f := Lookup(fd)
	if f == nil || !f.IsSock {
		return ErrINVAL
	}
	if f.RecvReader.Handle != 0 {
		f.RecvReader.Drop()
		f.RecvReader.Handle = 0
	}
	return nil
}

// SockCloseWrite closes the write direction of a socket.
func SockCloseWrite(fd int) error {
	f := Lookup(fd)
	if f == nil || !f.IsSock {
		return ErrINVAL
	}
	if f.SendWriter.Handle != 0 {
		f.SendWriter.Drop()
		f.SendWriter.Handle = 0
	}
	return nil
}

// --- await helpers ---
//
// awaitStream and awaitSubtask block the calling goroutine using the
// runtime poller, integrating with deadlines and Close.

// awaitStream parks the goroutine until the host delivers a callback for
// the given stream, or a deadline/Close fires.
// On deadline/close: cancels the in-flight CM operation and returns ErrBlocked.
// On success: returns the CM result (data already in the caller's buffer).
func awaitStream(handle int32, cancel func(int32) int32, ctx uintptr, mode int32) (int32, error) {
	slot := PollsetArm(handle, ctx, mode)

	pollErr := RuntimePollWait(ctx, int(mode))

	result := PollsetDisarm(slot)

	if pollErr != pollNoError {
		// Woken by deadline or Close, not by data.
		if result != 0 {
			return result, nil
		}
		// No data yet. Cancel the in-flight CM operation.
		if cancel != nil {
			cancelResult := cancel(handle)
			if cancelResult == wasi.Blocked {
				// Cancel pending — wait for the stream handle's
				// completion event to deliver the copy result.
				cancelResult = wasi.WaitForWaitable(handle)
			}
			cr, progress := wasi.UnpackCopy(cancelResult)
			if progress > 0 {
				return cancelResult, nil
			}
			if cr == wasi.CopyDropped {
				return cancelResult, nil
			}
		}
		return 0, ErrBlocked
	}

	return result, nil
}

// awaitSubtask parks the goroutine until an [async-lower] subtask completes,
// or a deadline/Close fires.
// On deadline/close: cancels the subtask and returns ErrBlocked.
// On success: returns nil (result is in the async retptr).
func awaitSubtask(subtask int32, ctx uintptr, mode int32) error {
	slot := PollsetArm(subtask, ctx, mode)

	pollErr := RuntimePollWait(ctx, int(mode))

	if pollErr != pollNoError {
		// Woken by deadline or Close, not by subtask completion.
		result := PollsetPeekResult(slot)
		if result != 0 {
			// Race: subtask completed before the deadline won.
			PollsetDisarm(slot)
			wasi.SubtaskDrop(subtask)
			return nil
		}
		// Subtask still pending. Cancel and wait.
		SubtaskCancelAndWait(subtask)
		return ErrBlocked
	}

	// Subtask completed normally. Disarm and drop it.
	PollsetDisarm(slot)
	wasi.SubtaskDrop(subtask)
	return nil
}

// --- Stream/conn setup ---

// SetupConnStreams sets up the receive and send streams for an established
// TCP connection. Returns stream handles and cancel function references.
func SetupConnStreams(sock int32) (recvReader TCPRecvReader, recvFuture TCPRecvFuture, sendWriter TCPSendWriter, sendFuture TCPSendFuture, err error) {
	recvReader, recvFuture = sockets.TCPSocket(sock).Receive()
	sendReader, sendWriter := NewTCPSendStreamPair()
	sendFuture = sockets.TCPSocket(sock).Send(sendReader)
	return
}

// DropConnStreams drops the receive and send stream handles.
// Used for cleanup when AllocSocketFD fails after SetupConnStreams succeeds.
func DropConnStreams(recvReader TCPRecvReader, recvFuture TCPRecvFuture, sendWriter TCPSendWriter, sendFuture TCPSendFuture) {
	sendWriter.Drop()
	sendFuture.Drop()
	recvReader.Drop()
	recvFuture.Drop()
}

// --- Blocking socket operations ---
//
// These functions perform complete CM I/O operations, blocking via
// awaitStream/awaitSubtask when needed. They return ErrBlocked on
// deadline/close interrupts, which maps to EAGAIN in syscall, causing
// poll.FD to call pd.waitRead/waitWrite → immediate deadline error.

// SockAcceptBlocking reads from the accept stream, sets up conn streams,
// and allocates the new FD. Returns a ready-to-use sysfd and sock handle.
func SockAcceptBlocking(fd int) (newSysfd int, sockHandle int32, err error) {
	f := Lookup(fd)
	if f == nil || !f.IsSock || f.AcceptReader.Handle == 0 {
		return -1, 0, ErrINVAL
	}

	var newSock [1]sockets.TCPSocket
	result := f.AcceptReader.Read(newSock[:])
	if result == wasi.Blocked {
		result, err = awaitStream(f.AcceptReader.Handle, f.readCancel, f.runtimeCtx, 'r')
		if err != nil {
			return -1, 0, err
		}
	}

	copyResult, progress := wasi.UnpackCopy(result)
	if copyResult == wasi.CopyDropped || copyResult == wasi.CopyCancelled || progress == 0 {
		return -1, 0, ErrClosed
	}

	sock := newSock[0]
	recvReader, recvFuture, sendWriter, sendFuture, err := SetupConnStreams(int32(sock))
	if err != nil {
		sock.Drop()
		return -1, 0, err
	}

	sysfd := AllocSocketFD(int32(sock), recvReader, recvFuture, sendWriter, sendFuture,
		sockets.TCPSocketReceiveStreamCancelRead0, sockets.TCPSocketSendStreamCancelWrite0)
	if sysfd < 0 {
		DropConnStreams(recvReader, recvFuture, sendWriter, sendFuture)
		sock.Drop()
		return -1, 0, ErrMFILE
	}

	return sysfd, int32(sock), nil
}

// SockConnectBlocking performs an async TCP connect, blocks until completion,
// and sets up conn streams on success.
func SockConnectBlocking(fd int, sock int32, addr sockets.IPSocketAddress) error {
	f := Lookup(fd)
	if f == nil {
		return ErrINVAL
	}

	status, result := sockets.TCPSocket(sock).ConnectAsync(addr)
	if !wasi.IsReturned(status) {
		subtask := wasi.Subtask(status)
		if err := awaitSubtask(subtask, f.runtimeCtx, 'w'); err != nil {
			wasi.FreeResult(unsafe.Pointer(result))
			return err
		}
	}
	defer wasi.FreeResult(unsafe.Pointer(result))

	if result.IsErr() {
		return sockError(result.Err())
	}

	// Set up streams now that connection is established.
	recvReader, recvFuture, sendWriter, sendFuture, err := SetupConnStreams(sock)
	if err != nil {
		return err
	}

	f.RecvReader = recvReader
	f.RecvFuture = recvFuture
	f.SendWriter = sendWriter
	f.SendFuture = sendFuture
	f.readCancel = sockets.TCPSocketReceiveStreamCancelRead0
	f.writeCancel = sockets.TCPSocketSendStreamCancelWrite0
	return nil
}

// UDPReceive calls UDPSocket.Receive, blocking via awaitSubtask if needed.
// Returns plain Go types — no WASI types escape to the caller.
func UDPReceive(fd int, buf []byte) (n int, ip [16]byte, port int, isIPv4 bool, err error) {
	f := Lookup(fd)
	if f == nil || !f.IsSock || !f.IsUDP {
		return 0, ip, 0, false, ErrINVAL
	}

	sock := sockets.UDPSocket(f.SockHandle)
	status, result := sock.ReceiveAsync()
	if !wasi.IsReturned(status) {
		subtask := wasi.Subtask(status)
		if waitErr := awaitSubtask(subtask, f.runtimeCtx, 'r'); waitErr != nil {
			wasi.FreeResult(unsafe.Pointer(result))
			return 0, ip, 0, false, waitErr
		}
	}
	defer wasi.FreeResult(unsafe.Pointer(result))

	if result.IsErr() {
		return 0, ip, 0, false, sockError(result.Err())
	}

	data, addr := result.OK()
	n = data.CopyInto(buf)
	ip, port, isIPv4 = parseIPSocketAddress(addr)
	return n, ip, port, isIPv4, nil
}

// UDPSend sends data to a specific remote address through the UDP socket.
func UDPSend(fd int, buf []byte, ip [16]byte, port int, isIPv4 bool) error {
	f := Lookup(fd)
	if f == nil || !f.IsSock || !f.IsUDP {
		return ErrINVAL
	}

	addr, err := buildIPSocketAddress(ip, port, isIPv4)
	if err != nil {
		return err
	}

	sock := sockets.UDPSocket(f.SockHandle)
	status, result := sock.SendAsync(buf, &addr)
	if !wasi.IsReturned(status) {
		subtask := wasi.Subtask(status)
		if waitErr := awaitSubtask(subtask, f.runtimeCtx, 'w'); waitErr != nil {
			wasi.FreeResult(unsafe.Pointer(result))
			return waitErr
		}
	}
	defer wasi.FreeResult(unsafe.Pointer(result))

	if result.IsErr() {
		return sockError(result.Err())
	}
	return nil
}

// UDPSendConnected sends data through a connected UDP socket (no address).
func UDPSendConnected(fd int, buf []byte) error {
	f := Lookup(fd)
	if f == nil || !f.IsSock || !f.IsUDP {
		return ErrINVAL
	}

	sock := sockets.UDPSocket(f.SockHandle)
	status, result := sock.SendAsync(buf, nil)
	if !wasi.IsReturned(status) {
		subtask := wasi.Subtask(status)
		if waitErr := awaitSubtask(subtask, f.runtimeCtx, 'w'); waitErr != nil {
			wasi.FreeResult(unsafe.Pointer(result))
			return waitErr
		}
	}
	defer wasi.FreeResult(unsafe.Pointer(result))

	if result.IsErr() {
		return sockError(result.Err())
	}
	return nil
}

// sockError wraps a WASI socket error code as a Go error.
func sockError(code sockets.ErrorCode) error {
	return SockErrorCode(code)
}

// SockErrorCode wraps a sockets error code as a Go error.
type SockErrorCode sockets.ErrorCode

func (e SockErrorCode) Error() string {
	switch sockets.ErrorCode(e) {
	case sockets.ErrorCodeAccessDenied:
		return "socket: access denied"
	case sockets.ErrorCodeNotSupported:
		return "socket: not supported"
	case sockets.ErrorCodeInvalidArgument:
		return "socket: invalid argument"
	case sockets.ErrorCodeOutOfMemory:
		return "socket: out of memory"
	case sockets.ErrorCodeTimeout:
		return "socket: timeout"
	case sockets.ErrorCodeInvalidState:
		return "socket: invalid state"
	case sockets.ErrorCodeAddressNotBindable:
		return "socket: address not bindable"
	case sockets.ErrorCodeAddressInUse:
		return "socket: address in use"
	case sockets.ErrorCodeRemoteUnreachable:
		return "socket: remote unreachable"
	case sockets.ErrorCodeConnectionRefused:
		return "socket: connection refused"
	case sockets.ErrorCodeConnectionReset:
		return "socket: connection reset"
	case sockets.ErrorCodeConnectionAborted:
		return "socket: connection aborted"
	case sockets.ErrorCodeDatagramTooLarge:
		return "socket: datagram too large"
	default:
		return "socket: unknown error"
	}
}

// ErrClosed is returned when operating on a closed socket.
var ErrClosed = closedError{}

type closedError struct{}

func (closedError) Error() string { return "use of closed connection" }

// ErrMFILE is returned when no file descriptor slots are available.
var ErrMFILE = mfileError{}

type mfileError struct{}

func (mfileError) Error() string { return "too many open files" }

// ErrBlocked is returned by SockRead/SockWrite when the CM stream operation
// blocks. The poll layer should arm the pollset and park the goroutine.
var ErrBlocked = blockedError{}

type blockedError struct{}

func (blockedError) Error() string { return "blocked" }

// ErrPipe is returned when writing to a closed socket.
var ErrPipe = pipeError{}

type pipeError struct{}

func (pipeError) Error() string { return "broken pipe" }

// ErrINVAL is returned for invalid operations.
var ErrINVAL = invalidError{}

type invalidError struct{}

func (invalidError) Error() string { return "invalid argument" }

// ErrInProgress is returned by SockConnectStart when the connect
// is started asynchronously (maps to EINPROGRESS in syscall).
var ErrInProgress = inProgressError{}

type inProgressError struct{}

func (inProgressError) Error() string { return "connection in progress" }

// --- Address helpers ---

// buildIPSocketAddress creates a WASI IPSocketAddress from Go IP bytes and port.
// Returns ErrINVAL if the port is out of the uint16 range.
func buildIPSocketAddress(ip [16]byte, port int, isIPv4 bool) (sockets.IPSocketAddress, error) {
	if port < 0 || port > 0xffff {
		return sockets.IPSocketAddress{}, ErrINVAL
	}
	var addr sockets.IPSocketAddress
	if isIPv4 {
		addr.SetIPV4(sockets.IPV4SocketAddress{
			Port:    uint16(port),
			Address: sockets.IPV4Address{F0: ip[12], F1: ip[13], F2: ip[14], F3: ip[15]},
		})
	} else {
		addr.SetIPV6(sockets.IPV6SocketAddress{
			Port: uint16(port),
			Address: sockets.IPV6Address{
				F0: uint16(ip[0])<<8 | uint16(ip[1]),
				F1: uint16(ip[2])<<8 | uint16(ip[3]),
				F2: uint16(ip[4])<<8 | uint16(ip[5]),
				F3: uint16(ip[6])<<8 | uint16(ip[7]),
				F4: uint16(ip[8])<<8 | uint16(ip[9]),
				F5: uint16(ip[10])<<8 | uint16(ip[11]),
				F6: uint16(ip[12])<<8 | uint16(ip[13]),
				F7: uint16(ip[14])<<8 | uint16(ip[15]),
			},
		})
	}
	return addr, nil
}

// parseIPSocketAddress extracts ip, port, and isIPv4 from a WASI IPSocketAddress.
func parseIPSocketAddress(addr sockets.IPSocketAddress) (ip [16]byte, port int, isIPv4 bool) {
	switch addr.Disc() {
	case sockets.IPSocketAddressIPV4:
		isIPv4 = true
		v4 := addr.IPV4()
		ip[10] = 0xff
		ip[11] = 0xff
		ip[12] = v4.Address.F0
		ip[13] = v4.Address.F1
		ip[14] = v4.Address.F2
		ip[15] = v4.Address.F3
		port = int(v4.Port)
	case sockets.IPSocketAddressIPV6:
		v6 := addr.IPV6()
		for i, seg := range []uint16{v6.Address.F0, v6.Address.F1, v6.Address.F2, v6.Address.F3, v6.Address.F4, v6.Address.F5, v6.Address.F6, v6.Address.F7} {
			ip[i*2] = byte(seg >> 8)
			ip[i*2+1] = byte(seg)
		}
		port = int(v6.Port)
	}
	return
}

// --- POSIX-style socket operations ---

// WASI address family constants.
const (
	wasiAFInet4 int32 = 0 // ip-address-family.ipv4
	wasiAFInet6 int32 = 1 // ip-address-family.ipv6
)

// SockSocket creates a new socket and allocates an FD for it.
// family is syscall.AF_INET (2) or syscall.AF_INET6 (3).
// sotype is syscall.SOCK_STREAM (1) or syscall.SOCK_DGRAM (2).
func SockSocket(family, sotype int) (int, error) {
	var afCode int32
	switch family {
	case 2: // AF_INET
		afCode = wasiAFInet4
	case 3: // AF_INET6
		afCode = wasiAFInet6
	default:
		return -1, ErrINVAL
	}

	if sotype == 2 { // SOCK_DGRAM
		createResult := sockets.NewUDPSocket(afCode)
		if createResult.IsErr() {
			return -1, sockError(createResult.Err())
		}
		sysfd := AllocUDPSocketFD(int32(createResult.OK()))
		if sysfd < 0 {
			createResult.OK().Drop()
			return -1, ErrMFILE
		}
		return sysfd, nil
	}

	// SOCK_STREAM (TCP)
	createResult := sockets.NewTCPSocket(afCode)
	if createResult.IsErr() {
		return -1, sockError(createResult.Err())
	}
	sock := createResult.OK()
	// Allocate FD with empty streams — streams are set up after connect/accept.
	sysfd := AllocSocketFD(int32(sock), TCPRecvReader{}, TCPRecvFuture{},
		TCPSendWriter{}, TCPSendFuture{}, nil, nil)
	if sysfd < 0 {
		sock.Drop()
		return -1, ErrMFILE
	}
	return sysfd, nil
}

// SockBind binds a socket to a local address.
func SockBind(fd int, ip [16]byte, port int, isIPv4 bool) error {
	f := Lookup(fd)
	if f == nil || !f.IsSock {
		return ErrINVAL
	}
	addr, err := buildIPSocketAddress(ip, port, isIPv4)
	if err != nil {
		return err
	}
	var result sockets.ResultVoidErrorCode
	if f.IsUDP {
		result = sockets.UDPSocket(f.SockHandle).Bind(addr)
	} else {
		result = sockets.TCPSocket(f.SockHandle).Bind(addr)
	}
	if result.IsErr() {
		return sockError(result.Err())
	}
	f.Bound = true
	return nil
}

// SockListen marks a TCP socket as listening.
func SockListen(fd int, backlog int) error {
	f := Lookup(fd)
	if f == nil || !f.IsSock || f.IsUDP {
		return ErrINVAL
	}
	sock := sockets.TCPSocket(f.SockHandle)

	// Set backlog size.
	backlogResult := sock.SetListenBacklogSize(int64(backlog))
	if backlogResult.IsErr() {
		return sockError(backlogResult.Err())
	}

	// Start listening — returns an accept stream.
	listenResult := sock.Listen()
	if listenResult.IsErr() {
		return sockError(listenResult.Err())
	}
	acceptReader := NewTCPAcceptStreamReader(listenResult.OK())

	f.AcceptReader = acceptReader
	f.readCancel = sockets.TCPSocketListenStreamCancelRead0
	return nil
}

// SockAccept accepts a connection from a listening socket.
// Returns the new FD, remote address ip/port/isIPv4, and any error.
func SockAccept(fd int) (newfd int, ip [16]byte, port int, isIPv4 bool, err error) {
	newSysfd, sockHandle, acceptErr := SockAcceptBlocking(fd)
	if acceptErr != nil {
		return -1, ip, 0, false, acceptErr
	}

	// Get remote address from the accepted socket.
	sock := sockets.TCPSocket(sockHandle)
	result := sock.GetRemoteAddress()
	if result.IsErr() {
		return newSysfd, ip, 0, false, sockError(result.Err())
	}
	ip, port, isIPv4 = parseIPSocketAddress(result.OK())

	return newSysfd, ip, port, isIPv4, nil
}

// SockGetsockname returns the local address of a socket.
func SockGetsockname(fd int) (ip [16]byte, port int, isIPv4 bool, err error) {
	f := Lookup(fd)
	if f == nil || !f.IsSock {
		return ip, 0, false, ErrINVAL
	}
	if f.IsUDP {
		result := sockets.UDPSocket(f.SockHandle).GetLocalAddress()
		if result.IsErr() {
			return ip, 0, false, sockError(result.Err())
		}
		ip, port, isIPv4 = parseIPSocketAddress(result.OK())
	} else {
		result := sockets.TCPSocket(f.SockHandle).GetLocalAddress()
		if result.IsErr() {
			return ip, 0, false, sockError(result.Err())
		}
		ip, port, isIPv4 = parseIPSocketAddress(result.OK())
	}
	return ip, port, isIPv4, nil
}

// SockGetpeername returns the remote address of a socket.
func SockGetpeername(fd int) (ip [16]byte, port int, isIPv4 bool, err error) {
	f := Lookup(fd)
	if f == nil || !f.IsSock {
		return ip, 0, false, ErrINVAL
	}
	if f.IsUDP {
		result := sockets.UDPSocket(f.SockHandle).GetRemoteAddress()
		if result.IsErr() {
			return ip, 0, false, sockError(result.Err())
		}
		ip, port, isIPv4 = parseIPSocketAddress(result.OK())
	} else {
		result := sockets.TCPSocket(f.SockHandle).GetRemoteAddress()
		if result.IsErr() {
			return ip, 0, false, sockError(result.Err())
		}
		ip, port, isIPv4 = parseIPSocketAddress(result.OK())
	}
	return ip, port, isIPv4, nil
}

// --- TCP Connect (EINPROGRESS mechanism) ---

// SockConnectStart starts an async TCP connect. If the connect completes
// synchronously, streams are set up and nil is returned. If the connect
// is pending, the subtask is stored in the CMFD and ErrInProgress is returned.
func SockConnectStart(fd int, ip [16]byte, port int, isIPv4 bool) error {
	f := Lookup(fd)
	if f == nil || !f.IsSock || f.IsUDP {
		return ErrINVAL
	}

	addr, addrErr := buildIPSocketAddress(ip, port, isIPv4)
	if addrErr != nil {
		return addrErr
	}
	sock := sockets.TCPSocket(f.SockHandle)
	status, result := sock.ConnectAsync(addr)

	if wasi.IsReturned(status) {
		// Completed synchronously.
		defer wasi.FreeResult(unsafe.Pointer(result))
		if result.IsErr() {
			return sockError(result.Err())
		}
		// Set up streams.
		recvReader, recvFuture, sendWriter, sendFuture, err := SetupConnStreams(f.SockHandle)
		if err != nil {
			return err
		}
		f.RecvReader = recvReader
		f.RecvFuture = recvFuture
		f.SendWriter = sendWriter
		f.SendFuture = sendFuture
		f.readCancel = sockets.TCPSocketReceiveStreamCancelRead0
		f.writeCancel = sockets.TCPSocketSendStreamCancelWrite0
		return nil
	}

	// Async — store subtask and result pointer for later.
	f.ConnectSubtask = wasi.Subtask(status)
	f.ConnectResult = unsafe.Pointer(result)
	return ErrInProgress
}

// ArmPendingConnect arms the pending TCP connect subtask in the pollset.
// Called from netpollopen (runtime) after Init registers the fd with the poller.
//
//go:linkname ArmPendingConnect
func ArmPendingConnect(fd int) {
	f := Lookup(fd)
	if f == nil || f.ConnectSubtask == 0 {
		return
	}
	PollsetArm(f.ConnectSubtask, f.runtimeCtx, 'w')
}

// SockGetConnectResult reads the result of a pending TCP connect.
// Called from GetsockoptInt(SOL_SOCKET, SO_ERROR).
// Returns nil on success (streams set up), SockErrorCode on connect error.
func SockGetConnectResult(fdNum int) error {
	f := Lookup(fdNum)
	if f == nil || f.ConnectSubtask == 0 {
		return nil // no pending connect
	}

	// Disarm the pollset entry.
	PollsetDisarm(f.ConnectSubtask)

	// Drop the subtask handle.
	wasi.SubtaskDrop(f.ConnectSubtask)

	// Read the connect result from the retptr.
	connectResult := (*sockets.ResultVoidErrorCode)(f.ConnectResult)

	// Clear fields before freeing — IsErr/ErrorCode must read before free.
	f.ConnectSubtask = 0
	var connectErr error
	if connectResult.IsErr() {
		connectErr = sockError(connectResult.Err())
	}
	wasi.FreeResult(f.ConnectResult)
	f.ConnectResult = nil

	if connectErr != nil {
		return connectErr
	}

	// Connect succeeded — set up streams.
	recvReader, recvFuture, sendWriter, sendFuture, err := SetupConnStreams(f.SockHandle)
	if err != nil {
		return err
	}
	f.RecvReader = recvReader
	f.RecvFuture = recvFuture
	f.SendWriter = sendWriter
	f.SendFuture = sendFuture
	f.readCancel = sockets.TCPSocketReceiveStreamCancelRead0
	f.writeCancel = sockets.TCPSocketSendStreamCancelWrite0
	return nil
}

// --- UDP Connect (synchronous) ---

// SockConnectSync performs a synchronous UDP connect.
func SockConnectSync(fd int, ip [16]byte, port int, isIPv4 bool) error {
	f := Lookup(fd)
	if f == nil || !f.IsSock || !f.IsUDP {
		return ErrINVAL
	}

	sock := sockets.UDPSocket(f.SockHandle)

	// WASI requires UDP sockets to be bound before connecting.
	if !f.Bound {
		var bindAddr sockets.IPSocketAddress
		if isIPv4 {
			bindAddr.SetIPV4(sockets.IPV4SocketAddress{})
		} else {
			bindAddr.SetIPV6(sockets.IPV6SocketAddress{})
		}
		bindResult := sock.Bind(bindAddr)
		if bindResult.IsErr() {
			return sockError(bindResult.Err())
		}
		f.Bound = true
	}

	addr, err := buildIPSocketAddress(ip, port, isIPv4)
	if err != nil {
		return err
	}
	result := sock.Connect(addr)
	if result.IsErr() {
		return sockError(result.Err())
	}
	return nil
}
