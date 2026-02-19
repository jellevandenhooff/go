// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package unix

import (
	"internal/wasi/posix"
	"syscall"
)

// sockErr converts internal/wasi/fd errors to syscall errors.
func sockErr(err error) error {
	if err == nil {
		return nil
	}
	switch err {
	case posix.ErrBlocked:
		return syscall.EAGAIN
	case posix.ErrPipe:
		return syscall.EPIPE
	case posix.ErrINVAL:
		return syscall.EINVAL
	}
	if e, ok := err.(posix.SockErrorCode); ok {
		return sockCodeToErrno(int(e))
	}
	return err
}

// WASI socket error code to errno mapping.
var wasiSockErrors = [...]syscall.Errno{
	0:  syscall.ENOTSUP,       // unknown
	1:  syscall.EACCES,        // access-denied
	2:  syscall.ENOTSUP,       // not-supported
	3:  syscall.EINVAL,        // invalid-argument
	4:  syscall.ENOMEM,        // out-of-memory
	5:  syscall.ETIMEDOUT,     // timeout
	6:  syscall.EINVAL,        // invalid-state
	7:  syscall.EADDRNOTAVAIL, // address-not-bindable
	8:  syscall.EADDRINUSE,    // address-in-use
	9:  syscall.EHOSTUNREACH,  // remote-unreachable
	10: syscall.ECONNREFUSED,  // connection-refused
	11: syscall.ECONNRESET,    // connection-reset
	12: syscall.ECONNABORTED,  // connection-aborted
	13: syscall.EMSGSIZE,      // datagram-too-large
}

func sockCodeToErrno(code int) syscall.Errno {
	if code < len(wasiSockErrors) && wasiSockErrors[code] != 0 {
		return wasiSockErrors[code]
	}
	return syscall.ENOTSUP
}

func RecvfromInet4(sysfd int, p []byte, flags int, from *syscall.SockaddrInet4) (int, error) {
	n, ip, port, _, err := posix.UDPReceive(sysfd, p)
	if err != nil {
		return 0, sockErr(err)
	}
	from.Port = port
	copy(from.Addr[:], ip[12:16])
	return n, nil
}

func RecvfromInet6(sysfd int, p []byte, flags int, from *syscall.SockaddrInet6) (int, error) {
	n, ip, port, _, err := posix.UDPReceive(sysfd, p)
	if err != nil {
		return 0, sockErr(err)
	}
	from.Port = port
	copy(from.Addr[:], ip[:])
	return n, nil
}

func SendtoInet4(sysfd int, p []byte, flags int, to *syscall.SockaddrInet4) error {
	var ip [16]byte
	ip[10] = 0xff
	ip[11] = 0xff
	copy(ip[12:], to.Addr[:])
	return sockErr(posix.UDPSend(sysfd, p, ip, to.Port, true))
}

func SendtoInet6(sysfd int, p []byte, flags int, to *syscall.SockaddrInet6) error {
	var ip [16]byte
	copy(ip[:], to.Addr[:])
	return sockErr(posix.UDPSend(sysfd, p, ip, to.Port, false))
}

func RecvmsgInet4(sysfd int, p, oob []byte, flags int, from *syscall.SockaddrInet4) (int, int, int, error) {
	n, err := RecvfromInet4(sysfd, p, flags, from)
	return n, 0, 0, err
}

func RecvmsgInet6(sysfd int, p, oob []byte, flags int, from *syscall.SockaddrInet6) (int, int, int, error) {
	n, err := RecvfromInet6(sysfd, p, flags, from)
	return n, 0, 0, err
}

func SendmsgNInet4(sysfd int, p, oob []byte, to *syscall.SockaddrInet4, flags int) (int, error) {
	if to == nil {
		// Connected socket -- send without address.
		if err := sockErr(posix.UDPSendConnected(sysfd, p)); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	if err := SendtoInet4(sysfd, p, flags, to); err != nil {
		return 0, err
	}
	return len(p), nil
}

func SendmsgNInet6(sysfd int, p, oob []byte, to *syscall.SockaddrInet6, flags int) (int, error) {
	if to == nil {
		// Connected socket -- send without address.
		if err := sockErr(posix.UDPSendConnected(sysfd, p)); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	if err := SendtoInet6(sysfd, p, flags, to); err != nil {
		return 0, err
	}
	return len(p), nil
}
