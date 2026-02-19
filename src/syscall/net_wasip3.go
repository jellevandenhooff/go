// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package syscall

import "internal/wasi/posix"

const (
	AF_UNSPEC = iota
	AF_UNIX
	AF_INET
	AF_INET6
)

const (
	SOCK_STREAM = 1 + iota
	SOCK_DGRAM
	SOCK_RAW
	SOCK_SEQPACKET
)

const (
	IPPROTO_IP   = 0
	IPPROTO_IPV4 = 4
	IPPROTO_IPV6 = 0x29
	IPPROTO_TCP  = 6
	IPPROTO_UDP  = 0x11
)

const (
	SOMAXCONN = 0x80
)

const (
	_ = iota
	IPV6_V6ONLY
	SO_ERROR
)

const (
	SOL_SOCKET = 1
)

const (
	_ = iota
	F_DUPFD_CLOEXEC
	SYS_FCNTL = 500 // unsupported
)

const (
	SHUT_RD   = 0x1
	SHUT_WR   = 0x2
	SHUT_RDWR = SHUT_RD | SHUT_WR
)

type Sockaddr any

type SockaddrInet4 struct {
	Port int
	Addr [4]byte
}

type SockaddrInet6 struct {
	Port   int
	ZoneId uint32
	Addr   [16]byte
}

type SockaddrUnix struct {
	Name string
}

// WASI socket error code to errno mapping.
var wasiSocketErrors = [...]Errno{
	0:  ENOTSUP,       // unknown
	1:  EACCES,        // access-denied
	2:  ENOTSUP,       // not-supported
	3:  EINVAL,        // invalid-argument
	4:  ENOMEM,        // out-of-memory
	5:  ETIMEDOUT,     // timeout
	6:  EINVAL,        // invalid-state
	7:  EADDRNOTAVAIL, // address-not-bindable
	8:  EADDRINUSE,    // address-in-use
	9:  EHOSTUNREACH,  // remote-unreachable
	10: ECONNREFUSED,  // connection-refused
	11: ECONNRESET,    // connection-reset
	12: ECONNABORTED,  // connection-aborted
	13: EMSGSIZE,      // datagram-too-large
}

// sockaddrToIPPort extracts ip bytes, port, and isIPv4 from a Sockaddr.
func sockaddrToIPPort(sa Sockaddr) (ip [16]byte, port int, isIPv4 bool) {
	switch sa := sa.(type) {
	case *SockaddrInet4:
		isIPv4 = true
		ip[10] = 0xff
		ip[11] = 0xff
		copy(ip[12:], sa.Addr[:])
		port = sa.Port
	case *SockaddrInet6:
		copy(ip[:], sa.Addr[:])
		port = sa.Port
	}
	return
}

// ipPortToSockaddr converts ip bytes, port, and isIPv4 to a Sockaddr.
func ipPortToSockaddr(ip [16]byte, port int, isIPv4 bool) Sockaddr {
	if isIPv4 {
		sa := &SockaddrInet4{Port: port}
		copy(sa.Addr[:], ip[12:16])
		return sa
	}
	sa := &SockaddrInet6{Port: port}
	copy(sa.Addr[:], ip[:])
	return sa
}

func Socket(domain, sotype, proto int) (int, error) {
	s, err := posix.SockSocket(domain, sotype)
	if err != nil {
		return -1, sockErrToSyscall(err)
	}
	return s, nil
}

func Bind(sysfd int, sa Sockaddr) error {
	ip, port, isIPv4 := sockaddrToIPPort(sa)
	return sockErrToSyscall(posix.SockBind(sysfd, ip, port, isIPv4))
}

func StopIO(sysfd int) error {
	return ENOSYS
}

func Listen(sysfd int, backlog int) error {
	return sockErrToSyscall(posix.SockListen(sysfd, backlog))
}

func Accept(sysfd int) (int, Sockaddr, error) {
	newfd, ip, port, isIPv4, err := posix.SockAccept(sysfd)
	if err != nil {
		return -1, nil, sockErrToSyscall(err)
	}
	return newfd, ipPortToSockaddr(ip, port, isIPv4), nil
}

func Connect(sysfd int, sa Sockaddr) error {
	ip, port, isIPv4 := sockaddrToIPPort(sa)

	f := posix.Lookup(sysfd)
	if f != nil && f.IsUDP {
		return sockErrToSyscall(posix.SockConnectSync(sysfd, ip, port, isIPv4))
	}

	err := posix.SockConnectStart(sysfd, ip, port, isIPv4)
	if err == posix.ErrInProgress {
		return EINPROGRESS
	}
	return sockErrToSyscall(err)
}

func Getsockname(sysfd int) (Sockaddr, error) {
	ip, port, isIPv4, err := posix.SockGetsockname(sysfd)
	if err != nil {
		return nil, sockErrToSyscall(err)
	}
	return ipPortToSockaddr(ip, port, isIPv4), nil
}

func Getpeername(sysfd int) (Sockaddr, error) {
	ip, port, isIPv4, err := posix.SockGetpeername(sysfd)
	if err != nil {
		return nil, sockErrToSyscall(err)
	}
	return ipPortToSockaddr(ip, port, isIPv4), nil
}

func GetsockoptInt(sysfd, level, opt int) (int, error) {
	if level == SOL_SOCKET && opt == SO_ERROR {
		err := posix.SockGetConnectResult(sysfd)
		if err == nil {
			return 0, nil
		}
		if e, ok := err.(posix.SockErrorCode); ok {
			code := int(e)
			if code < len(wasiSocketErrors) {
				return int(wasiSocketErrors[code]), nil
			}
			return int(Errno(0xffff)), nil
		}
		return 0, sockErrToSyscall(err)
	}
	return 0, ENOSYS
}

func SetsockoptInt(sysfd, level, opt int, value int) error {
	return ENOSYS
}

func SetReadDeadline(sysfd int, t int64) error {
	return ENOSYS
}

func SetWriteDeadline(sysfd int, t int64) error {
	return ENOSYS
}

func Recvfrom(sysfd int, p []byte, flags int) (int, Sockaddr, error) {
	n, ip, port, isIPv4, err := posix.UDPReceive(sysfd, p)
	if err != nil {
		return 0, nil, sockErrToSyscall(err)
	}
	return n, ipPortToSockaddr(ip, port, isIPv4), nil
}

func Sendto(sysfd int, p []byte, flags int, to Sockaddr) error {
	ip, port, isIPv4 := sockaddrToIPPort(to)
	return sockErrToSyscall(posix.UDPSend(sysfd, p, ip, port, isIPv4))
}

func Recvmsg(sysfd int, p, oob []byte, flags int) (n, oobn, recvflags int, from Sockaddr, err error) {
	n, from, err = Recvfrom(sysfd, p, flags)
	return n, 0, 0, from, err
}

func SendmsgN(sysfd int, p, oob []byte, to Sockaddr, flags int) (int, error) {
	if to != nil {
		if err := Sendto(sysfd, p, flags, to); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	// Connected socket — send without address.
	err := sockErrToSyscall(posix.UDPSendConnected(sysfd, p))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func Shutdown(sysfd int, how int) error {
	if how&SHUT_RD != 0 {
		if err := posix.SockCloseRead(sysfd); err != nil {
			return sockErrToSyscall(err)
		}
	}
	if how&SHUT_WR != 0 {
		if err := posix.SockCloseWrite(sysfd); err != nil {
			return sockErrToSyscall(err)
		}
	}
	return nil
}
