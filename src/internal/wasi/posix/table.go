// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

// Package posix manages file descriptors for wasip3, mapping Go integer FDs
// to Component Model descriptor and socket resource handles.
package posix

import (
	"internal/wasi/generated/filesystem"
	"unsafe"
)

// CMFD represents a Component Model file descriptor entry.
type CMFD struct {
	Desc    filesystem.Descriptor // CM descriptor resource handle
	Pos     int64                 // current file position
	IsDir   bool                  // cached: is this a directory?
	IsPreop bool                  // preopen directory (don't drop on close)

	// Socket fields (set for socket FDs)
	IsSock       bool
	IsUDP        bool            // true for UDP sockets (no streams)
	SockHandle   int32           // tcp-socket or udp-socket resource handle
	RecvReader   TCPRecvReader   // receive stream reader
	RecvFuture   TCPRecvFuture   // receive future (error result)
	SendWriter   TCPSendWriter   // send stream writer
	SendFuture   TCPSendFuture   // pending send future (result of tcp-socket.send)
	AcceptReader TCPAcceptReader // accept stream reader (listener)

	// Socket state
	Bound          bool           // set by SockBind
	ConnectSubtask int32          // pending TCP connect subtask
	ConnectResult  unsafe.Pointer // async result pointer (freed after read)

	// Runtime poller context, set by runtime.netpollopen via linkname.
	runtimeCtx uintptr // pollDesc pointer from runtime poller

	// CM stream cancel functions (async-lower), set during socket creation.
	// Returns the copy result if completed, or wasi.Blocked (-1) if pending.
	readCancel  func(int32) int32 // [async-lower]stream-cancel-read-0
	writeCancel func(int32) int32 // [async-lower]stream-cancel-write-0

	// Pipe fields (set for in-memory pipe FDs used by exec)
	IsPipe    bool
	PipeRead  *PipeBuf // non-nil on the read end
	PipeWrite *PipeBuf // non-nil on the write end
}

const MaxFDs = 256

var (
	FDTable    [MaxFDs]*CMFD
	FDFreeHead int32 = -1 // head of free list (-1 = empty)
	fdFreeNext [MaxFDs]int32
)

func FDTableInit() {
	// Chain indices 3..MaxFDs-1 into a free list (0,1,2 = stdio).
	for i := int32(3); i < MaxFDs-1; i++ {
		fdFreeNext[i] = i + 1
	}
	fdFreeNext[MaxFDs-1] = -1
	FDFreeHead = 3
}

// FDAlloc allocates a file descriptor slot from the free list.
// Returns -1 if no slots are available.
func FDAlloc() int {
	if FDFreeHead == -1 {
		return -1
	}
	fd := FDFreeHead
	FDFreeHead = fdFreeNext[fd]
	FDTable[fd] = &CMFD{}
	return int(fd)
}

func AllocFD(desc filesystem.Descriptor, isDir bool) int {
	fd := FDAlloc()
	if fd < 0 {
		return -1
	}
	FDTable[fd].Desc = desc
	FDTable[fd].IsDir = isDir
	return fd
}

// SetRuntimeCtx stores the runtime poller context for a file descriptor.
// Called from runtime.netpollopen when the fd is registered with the poller.
//
//go:linkname SetRuntimeCtx
func SetRuntimeCtx(fd int, ctx uintptr) {
	if fd >= 0 && fd < MaxFDs && FDTable[fd] != nil {
		FDTable[fd].runtimeCtx = ctx
	}
}

func Lookup(fd int) *CMFD {
	if fd < 0 || fd >= MaxFDs {
		return nil
	}
	return FDTable[fd]
}

func Free(fd int) {
	if fd >= 0 && fd < MaxFDs {
		FDTable[fd] = nil
		fdFreeNext[fd] = FDFreeHead
		FDFreeHead = int32(fd)
	}
}

// DescriptorTypeToFiletype maps wasip3 descriptor-type values to
// wasip1 FILETYPE_* constants used by stat_wasi.go.
func DescriptorTypeToFiletype(dt filesystem.DescriptorType) uint8 {
	const (
		FILETYPE_UNKNOWN          = 0
		FILETYPE_BLOCK_DEVICE     = 1
		FILETYPE_CHARACTER_DEVICE = 2
		FILETYPE_DIRECTORY        = 3
		FILETYPE_REGULAR_FILE     = 4
		FILETYPE_SOCKET_DGRAM     = 5
		FILETYPE_SOCKET_STREAM    = 6
		FILETYPE_SYMBOLIC_LINK    = 7
	)
	switch dt {
	case filesystem.DescriptorTypeBlockDevice:
		return FILETYPE_BLOCK_DEVICE
	case filesystem.DescriptorTypeCharacterDevice:
		return FILETYPE_CHARACTER_DEVICE
	case filesystem.DescriptorTypeDirectory:
		return FILETYPE_DIRECTORY
	case filesystem.DescriptorTypeRegularFile:
		return FILETYPE_REGULAR_FILE
	case filesystem.DescriptorTypeSymbolicLink:
		return FILETYPE_SYMBOLIC_LINK
	case filesystem.DescriptorTypeSocket:
		return FILETYPE_SOCKET_STREAM
	case filesystem.DescriptorTypeFifo:
		return FILETYPE_UNKNOWN // no wasip1 equivalent
	default:
		return FILETYPE_UNKNOWN
	}
}

// FDStatGetFlags returns the fdflags for a given FD.
func FDStatGetFlags(goFd int) (uint32, error) {
	f := Lookup(goFd)
	if f == nil {
		return 0, ErrBADF
	}
	if f.IsPipe {
		return 0, nil // pipes have no special flags
	}
	result := f.Desc.GetFlags()
	if result.IsErr() {
		return 0, FSErrorCode(result.Err())
	}
	var flags uint32
	descFlags := result.OK()
	if descFlags&(1<<2) != 0 { // file-integrity-sync
		flags |= 0x0010 // FDFLAG_SYNC
	}
	if descFlags&(1<<3) != 0 { // data-integrity-sync
		flags |= 0x0002 // FDFLAG_DSYNC
	}
	return flags, nil
}

// FDStatGetType returns the file type for a given FD.
func FDStatGetType(goFd int) (uint8, error) {
	f := Lookup(goFd)
	if f == nil {
		return 0, ErrBADF
	}
	if f.IsPipe {
		return 0, nil // FILETYPE_UNKNOWN for pipes
	}
	result := f.Desc.GetType()
	if result.IsErr() {
		return 0, FSErrorCode(result.Err())
	}
	return uint8(result.OK()), nil
}

// ErrBADF is returned when a file descriptor is invalid.
var ErrBADF error = FSErrorCode(filesystem.ErrorCodeBadDescriptor)

// FSErrorCode wraps a filesystem error code as a Go error.
type FSErrorCode filesystem.ErrorCode

func (e FSErrorCode) Error() string {
	switch filesystem.ErrorCode(e) {
	case filesystem.ErrorCodeAccess:
		return "filesystem: access denied"
	case filesystem.ErrorCodeBadDescriptor:
		return "filesystem: bad descriptor"
	case filesystem.ErrorCodeBusy:
		return "filesystem: busy"
	case filesystem.ErrorCodeExist:
		return "filesystem: already exists"
	case filesystem.ErrorCodeIO:
		return "filesystem: I/O error"
	case filesystem.ErrorCodeIsDirectory:
		return "filesystem: is a directory"
	case filesystem.ErrorCodeNoEntry:
		return "filesystem: no such file or directory"
	case filesystem.ErrorCodeNotDirectory:
		return "filesystem: not a directory"
	case filesystem.ErrorCodeNotEmpty:
		return "filesystem: directory not empty"
	case filesystem.ErrorCodeNotPermitted:
		return "filesystem: not permitted"
	case filesystem.ErrorCodeReadOnly:
		return "filesystem: read-only filesystem"
	case filesystem.ErrorCodeInvalid:
		return "filesystem: invalid argument"
	case filesystem.ErrorCodeLoop:
		return "filesystem: too many symlinks"
	case filesystem.ErrorCodeNameTooLong:
		return "filesystem: name too long"
	case filesystem.ErrorCodeInsufficientSpace:
		return "filesystem: no space left"
	case filesystem.ErrorCodePipe:
		return "filesystem: broken pipe"
	case filesystem.ErrorCodeInvalidSeek:
		return "filesystem: invalid seek"
	default:
		return "filesystem: unknown error"
	}
}

// Preopens management.
type Opendir struct {
	FD   int32
	Name string
}

var Preopens []Opendir

func InitPreopens() {
	dirs := filesystem.GetDirectories()
	for _, d := range dirs {
		goFd := AllocFD(d.F0, true)
		if goFd < 0 {
			continue
		}
		FDTable[goFd].IsPreop = true

		Preopens = append(Preopens, Opendir{
			FD:   int32(goFd),
			Name: d.F1,
		})
	}
}
