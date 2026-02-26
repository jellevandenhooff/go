// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package posix

import (
	"internal/stringslite"
	"internal/wasi"
	"internal/wasi/generated/cli"
	"internal/wasi/generated/filesystem"
	"unsafe"
)

// Cwd is the current working directory, set during init by the syscall package.
var Cwd string

// --- Path helpers (duplicated from syscall/path_wasi.go for use in posix) ---

func isAbs(path string) bool {
	return stringslite.HasPrefix(path, "/")
}

func isDir(path string) bool {
	return stringslite.HasSuffix(path, "/")
}

func appendCleanPath(buf []byte, path string, lookupParent bool) ([]byte, bool) {
	i := 0
	for i < len(path) {
		for i < len(path) && path[i] == '/' {
			i++
		}

		j := i
		for j < len(path) && path[j] != '/' {
			j++
		}

		s := path[i:j]
		i = j

		switch s {
		case "":
			continue
		case ".":
			continue
		case "..":
			if !lookupParent {
				k := len(buf)
				for k > 0 && buf[k-1] != '/' {
					k--
				}
				for k > 1 && buf[k-1] == '/' {
					k--
				}
				buf = buf[:k]
				if k == 0 {
					lookupParent = true
				} else {
					s = ""
					continue
				}
			}
		default:
			lookupParent = false
		}

		if len(buf) > 0 && buf[len(buf)-1] != '/' {
			buf = append(buf, '/')
		}
		buf = append(buf, s...)
	}
	return buf, lookupParent
}

func joinPath(dir, file string) string {
	buf := make([]byte, 0, len(dir)+len(file)+1)
	if isAbs(dir) {
		buf = append(buf, '/')
	}
	buf, lookupParent := appendCleanPath(buf, dir, false)
	buf, _ = appendCleanPath(buf, file, lookupParent)
	if len(buf) == 0 {
		buf = append(buf, '.')
	}
	if buf[len(buf)-1] != '/' && isDir(file) {
		buf = append(buf, '/')
	}
	return unsafe.String(&buf[0], len(buf))
}

// --- Path resolution ---

// PreparePath returns the preopen file descriptor and the relative path
// for a given absolute or CWD-relative path.
func PreparePath(path string) (int32, string) {
	var dirFd = int32(-1)
	var dirName string

	dir := "/"
	if !isAbs(path) {
		dir = Cwd
	}
	path = joinPath(dir, path)

	for _, p := range Preopens {
		if len(p.Name) > len(dirName) && stringslite.HasPrefix(path, p.Name) {
			dirFd, dirName = p.FD, p.Name
		}
	}

	path = path[len(dirName):]
	for isAbs(path) {
		path = path[1:]
	}
	if len(path) == 0 {
		path = "."
	}

	return dirFd, path
}

// PreparePathDesc returns the CM descriptor handle and the relative path
// for use in CM filesystem calls.
func PreparePathDesc(path string) (filesystem.Descriptor, string) {
	goFd, relPath := PreparePath(path)
	f := Lookup(int(goFd))
	if f == nil {
		return -1, relPath
	}
	return f.Desc, relPath
}

// --- Error helper ---

func fsErr(code filesystem.ErrorCode) error { return FSErrorCode(code) }

// --- Core file I/O ---

// FileOpen opens a file given its path, resolving against Cwd and preopens.
func FileOpen(path string, isAppend bool, pathFlags, openFlags, descFlags int32) (int, error) {
	dirFD, relPath := PreparePath(path)
	return fileOpenAt(dirFD, relPath, isAppend, pathFlags, openFlags, descFlags)
}

// FileOpenAt opens a file given a directory FD and relative path.
func FileOpenAt(dirFD int, path string, isAppend bool, pathFlags, openFlags, descFlags int32) (int, error) {
	return fileOpenAt(int32(dirFD), path, isAppend, pathFlags, openFlags, descFlags)
}

func fileOpenAt(dirFD int32, path string, isAppend bool, pathFlags, openFlags, descFlags int32) (int, error) {
	f := Lookup(int(dirFD))
	if f == nil {
		return -1, ErrBADF
	}

	result := f.Desc.OpenAt(pathFlags, path, openFlags, descFlags)
	if result.IsErr() {
		return -1, fsErr(result.Err())
	}

	newDesc := result.OK()
	isDirectory := openFlags&int32(filesystem.OpenFlagsDirectory) != 0
	if !isDirectory {
		typeResult := newDesc.GetType()
		if !typeResult.IsErr() {
			isDirectory = typeResult.OK() == filesystem.DescriptorTypeDirectory
		}
	}
	goFd := AllocFD(newDesc, isDirectory)
	if goFd < 0 {
		newDesc.Drop()
		return -1, ErrMFILE
	}

	if isAppend {
		FDTable[goFd].Pos = -1
	}

	return goFd, nil
}

// FileClose closes a non-socket file descriptor.
func FileClose(fd int) error {
	f := Lookup(fd)
	if f == nil {
		return ErrBADF
	}
	if !f.IsPreop {
		f.Desc.Drop()
	}
	Free(fd)
	return nil
}

// FileRead reads from a file at a given offset using CM streams.
func FileRead(f *CMFD, b []byte, offset int64) (int, error) {
	reader, future := f.Desc.ReadViaStream(offset)
	n, result := wasi.ReadStreamBytes(reader, future, b)
	if result.IsErr() {
		return n, fsErr(result.Err())
	}
	return n, nil
}

// FileWrite writes to a file at a given offset using CM streams.
func FileWrite(f *CMFD, b []byte, offset int64) (int, error) {
	reader, writer := wasi.NewStreamPair[filesystem.DescriptorWriteViaStreamStreamOps0, byte]()
	future := f.Desc.WriteViaStream(reader, offset)
	n, result := wasi.WriteStreamBytes(writer, future, b)
	if result.IsErr() {
		return n, fsErr(result.Err())
	}
	return n, nil
}

// FileAppend writes to a file in append mode.
func FileAppend(f *CMFD, b []byte) (int, error) {
	reader, writer := wasi.NewStreamPair[filesystem.DescriptorAppendViaStreamStreamOps0, byte]()
	future := f.Desc.AppendViaStream(reader)
	n, result := wasi.WriteStreamBytes(writer, future, b)
	if result.IsErr() {
		return n, fsErr(result.Err())
	}
	return n, nil
}

// FileSeek computes a new file position based on offset and whence.
// Whence: 0=SET, 1=CUR, 2=END.
func FileSeek(f *CMFD, offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case 0: // SET
		newPos = offset
	case 1: // CUR
		if f.Pos < 0 {
			return 0, fsErr(filesystem.ErrorCodeInvalidSeek)
		}
		newPos = f.Pos + offset
	case 2: // END
		statResult := f.Desc.Stat()
		if statResult.IsErr() {
			return 0, fsErr(statResult.Err())
		}
		sz := int64(statResult.OK().Size)
		newPos = sz + offset
	default:
		return 0, fsErr(filesystem.ErrorCodeInvalid)
	}

	if newPos < 0 {
		return 0, fsErr(filesystem.ErrorCodeInvalid)
	}
	f.Pos = newPos
	return newPos, nil
}

// FileSetSize sets the size of a file (truncate/extend).
func FileSetSize(f *CMFD, size int64) error {
	result := f.Desc.SetSize(size)
	if result.IsErr() {
		return fsErr(result.Err())
	}
	return nil
}

// FileSync synchronizes file data and metadata to storage.
func FileSync(f *CMFD) error {
	result := f.Desc.Sync()
	if result.IsErr() {
		return fsErr(result.Err())
	}
	return nil
}

// --- Stat operations ---

// FileStat returns the stat for a file descriptor.
func FileStat(f *CMFD) (filesystem.DescriptorStatT, error) {
	result := f.Desc.Stat()
	if result.IsErr() {
		return filesystem.DescriptorStatT{}, fsErr(result.Err())
	}
	return result.OK(), nil
}

// FileStatAt returns the stat for a path relative to a descriptor.
func FileStatAt(desc filesystem.Descriptor, pathFlags int32, path string) (filesystem.DescriptorStatT, error) {
	result := desc.StatAt(pathFlags, path)
	if result.IsErr() {
		return filesystem.DescriptorStatT{}, fsErr(result.Err())
	}
	return result.OK(), nil
}

// --- Directory operations ---

// DirEntry represents a directory entry returned by ReadDirEntries.
type DirEntry struct {
	Type uint8
	Name string
}

// ReadDirEntries returns typed directory entries for the given directory fd.
func ReadDirEntries(fd int) ([]DirEntry, error) {
	f := Lookup(fd)
	if f == nil {
		return nil, ErrBADF
	}
	if !f.IsDir {
		return nil, fsErr(filesystem.ErrorCodeNotDirectory)
	}

	streamReader, futureReader := f.Desc.ReadDirectory()
	streamHandle := streamReader.Handle
	futureHandle := futureReader.Handle

	var entries []DirEntry

	for {
		var entryBuf struct {
			_       [0]int32
			typ     uint8
			_pad    [3]byte
			namePtr int32
			nameLen int32
		}
		result := filesystem.DescriptorReadDirectoryStreamRead0(streamHandle, unsafe.Pointer(&entryBuf), 1)
		if result == wasi.Blocked {
			result = wasi.WaitForWaitable(streamHandle)
		}

		copyResult, nEntries := wasi.UnpackCopy(result)
		if nEntries > 0 {
			name := wasi.CopyString(entryBuf.namePtr, entryBuf.nameLen)
			entries = append(entries, DirEntry{Type: entryBuf.typ, Name: name})
		}

		if copyResult == wasi.CopyDropped || nEntries == 0 {
			break
		}
	}

	filesystem.DescriptorReadDirectoryStreamDropReadable0(streamHandle)

	var futBuf struct {
		_    [0]int32
		data [filesystem.ResultVoidErrorCodeSize]byte
	}
	futStatus := filesystem.DescriptorReadDirectoryFutureRead1(futureHandle, unsafe.Pointer(&futBuf))
	if futStatus == wasi.Blocked {
		wasi.WaitForWaitable(futureHandle)
	}
	filesystem.DescriptorReadDirectoryFutureDropReadable1(futureHandle)

	if futBuf.data[0] != 0 {
		return entries, fsErr(filesystem.ErrorCode(futBuf.data[1]))
	}
	return entries, nil
}

// --- Path operations ---

// CreateDirAt creates a directory at a path relative to a descriptor.
func CreateDirAt(desc filesystem.Descriptor, path string) error {
	result := desc.CreateDirectoryAt(path)
	if result.IsErr() {
		return fsErr(result.Err())
	}
	return nil
}

// UnlinkFileAt removes a file at a path relative to a descriptor.
func UnlinkFileAt(desc filesystem.Descriptor, path string) error {
	result := desc.UnlinkFileAt(path)
	if result.IsErr() {
		return fsErr(result.Err())
	}
	return nil
}

// RemoveDirAt removes a directory at a path relative to a descriptor.
func RemoveDirAt(desc filesystem.Descriptor, path string) error {
	result := desc.RemoveDirectoryAt(path)
	if result.IsErr() {
		return fsErr(result.Err())
	}
	return nil
}

// ReadlinkAt reads the target of a symlink at a path relative to a descriptor.
func ReadlinkAt(desc filesystem.Descriptor, path string) (string, error) {
	result := desc.ReadlinkAt(path)
	if result.IsErr() {
		return "", fsErr(result.Err())
	}
	return result.OK(), nil
}

// RenameAt renames a file from one path to another, potentially across descriptors.
func RenameAt(oldDesc filesystem.Descriptor, oldPath string, newDesc filesystem.Descriptor, newPath string) error {
	result := oldDesc.RenameAt(oldPath, int32(newDesc), newPath)
	if result.IsErr() {
		return fsErr(result.Err())
	}
	return nil
}

// LinkAt creates a hard link from one path to another.
func LinkAt(oldDesc filesystem.Descriptor, oldPath string, newDesc filesystem.Descriptor, newPath string, pathFlags int32) error {
	result := oldDesc.LinkAt(pathFlags, oldPath, int32(newDesc), newPath)
	if result.IsErr() {
		return fsErr(result.Err())
	}
	return nil
}

// SymlinkAt creates a symbolic link at a path relative to a descriptor.
func SymlinkAt(target string, desc filesystem.Descriptor, path string) error {
	result := desc.SymlinkAt(target, path)
	if result.IsErr() {
		return fsErr(result.Err())
	}
	return nil
}

// SetTimesAt sets timestamps on a file at a path relative to a descriptor.
func SetTimesAt(desc filesystem.Descriptor, pathFlags int32, path string, atime, mtime filesystem.NewTimestamp) error {
	result := desc.SetTimesAt(pathFlags, path, atime, mtime)
	if result.IsErr() {
		return fsErr(result.Err())
	}
	return nil
}

// --- Stdio ---

// StdioWrite writes data to stdout (fd=1) or stderr (fd=2) using the
// wasi:cli/stdout or wasi:cli/stderr write-via-stream function.
func StdioWrite(fd int, b []byte) (int, error) {
	var n int
	var result cli.ResultVoidErrorCode
	if fd == 1 {
		reader, writer := wasi.NewStreamPair[cli.StdoutWriteViaStreamStreamOps0, byte]()
		future := cli.StdoutWriteViaStream(reader)
		n, result = wasi.WriteStreamBytes(writer, future, b)
	} else {
		reader, writer := wasi.NewStreamPair[cli.StderrWriteViaStreamStreamOps0, byte]()
		future := cli.StderrWriteViaStream(reader)
		n, result = wasi.WriteStreamBytes(writer, future, b)
	}
	if result.IsErr() {
		return n, fsErr(filesystem.ErrorCodeIO)
	}
	return n, nil
}
