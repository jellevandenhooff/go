// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package syscall

import (
	"internal/wasi/generated/clocks"
	"internal/wasi/generated/filesystem"
	"internal/wasi/posix"
)

// utimeOmit indicates that a timestamp should not be changed.
const utimeOmit = -0x2

const atSymlinkNofollow = 0x100

// atPathFlags converts AT_SYMLINK_NOFOLLOW-style flags to WASI path-flags.
// When AT_SYMLINK_NOFOLLOW is not set, symlinks are followed.
func atPathFlags(flags int) int32 {
	if flags&atSymlinkNofollow == 0 {
		return int32(filesystem.PathFlagsSymlinkFollow)
	}
	return 0
}

// --- Filesystem error code mapping ---
// Maps wasi:filesystem/types error-code enum to Go Errno.

var fsErrors = [...]Errno{
	0:  EACCES,       // access
	1:  EALREADY,     // already
	2:  EBADF,        // bad-descriptor
	3:  EBUSY,        // busy
	4:  EDEADLK,      // deadlock
	5:  EDQUOT,       // quota
	6:  EEXIST,       // exist
	7:  EFBIG,        // file-too-large
	8:  EILSEQ,       // illegal-byte-sequence
	9:  EINPROGRESS,  // in-progress
	10: EINTR,        // interrupted
	11: EINVAL,       // invalid
	12: EIO,          // io
	13: EISDIR,       // is-directory
	14: ELOOP,        // loop
	15: EMLINK,       // too-many-links
	16: EMSGSIZE,     // message-size
	17: ENAMETOOLONG, // name-too-long
	18: ENODEV,       // no-device
	19: ENOENT,       // no-entry
	20: ENOLCK,       // no-lock
	21: ENOMEM,       // insufficient-memory
	22: ENOSPC,       // insufficient-space
	23: ENOTDIR,      // not-directory
	24: ENOTEMPTY,    // not-empty
	25: EIO,          // not-recoverable (no direct mapping)
	26: ENOTSUP,      // unsupported
	27: EIO,          // no-tty (no direct mapping)
	28: ENODEV,       // no-such-device
	29: EOVERFLOW,    // overflow
	30: EPERM,        // not-permitted
	31: EPIPE,        // pipe
	32: EROFS,        // read-only
	33: ESPIPE,       // invalid-seek
	34: ETXTBSY,      // text-file-busy
	35: EXDEV,        // cross-device
}

func fsError(code filesystem.ErrorCode) Errno {
	if int(code) < len(fsErrors) {
		return fsErrors[code]
	}
	return Errno(0xffff)
}

// fsErrToSyscall converts posix filesystem errors to syscall Errno values.
func fsErrToSyscall(err error) error {
	if err == nil {
		return nil
	}
	if e, ok := err.(posix.FSErrorCode); ok {
		return fsError(filesystem.ErrorCode(e))
	}
	if err == posix.ErrMFILE {
		return EMFILE
	}
	return err
}

// --- Flag conversion ---

// convertFlags extracts WASI open-flags and descriptor-flags from a Go openmode.
func convertFlags(openmode int) (openFlags, descFlags int32) {
	if openmode&O_CREATE != 0 {
		openFlags |= int32(filesystem.OpenFlagsCreate)
	}
	if openmode&O_DIRECTORY != 0 {
		openFlags |= int32(filesystem.OpenFlagsDirectory)
	}
	if openmode&O_EXCL != 0 {
		openFlags |= int32(filesystem.OpenFlagsExclusive)
	}
	if openmode&O_TRUNC != 0 {
		openFlags |= int32(filesystem.OpenFlagsTruncate)
	}

	switch openmode & (O_RDONLY | O_WRONLY | O_RDWR) {
	case O_RDONLY:
		descFlags = int32(filesystem.DescriptorFlagsRead)
	case O_WRONLY:
		descFlags = int32(filesystem.DescriptorFlagsWrite)
	case O_RDWR:
		descFlags = int32(filesystem.DescriptorFlagsRead | filesystem.DescriptorFlagsWrite)
	}
	if openmode&O_SYNC != 0 {
		descFlags |= int32(filesystem.DescriptorFlagsFileIntegritySync)
	}
	return
}

// convertTimestamps converts two Timespec values to WASI NewTimestamp values.
func convertTimestamps(a, m Timespec) (atime, mtime filesystem.NewTimestamp) {
	if a.Nsec == utimeOmit {
		atime.SetNoChange()
	} else {
		atime.SetTimestamp(clocks.Instant{Seconds: a.Sec, Nanoseconds: uint32(a.Nsec)})
	}
	if m.Nsec == utimeOmit {
		mtime.SetNoChange()
	} else {
		mtime.SetTimestamp(clocks.Instant{Seconds: m.Sec, Nanoseconds: uint32(m.Nsec)})
	}
	return
}

// --- Type aliases matching fs_wasi.go ---

type timestamp = uint64
type size = uint32
type fdflags = uint32
type filetype = uint8
type rights = uint64
type dircookie = uint64

// --- Constants matching fs_wasi.go for compatibility ---

const (
	LOOKUP_SYMLINK_FOLLOW = 0x00000001
)

const (
	OFLAG_CREATE    = 0x0001
	OFLAG_DIRECTORY = 0x0002
	OFLAG_EXCL      = 0x0004
	OFLAG_TRUNC     = 0x0008
)

const (
	FDFLAG_APPEND   = 0x0001
	FDFLAG_DSYNC    = 0x0002
	FDFLAG_NONBLOCK = 0x0004
	FDFLAG_RSYNC    = 0x0008
	FDFLAG_SYNC     = 0x0010
)

const (
	WHENCE_SET = 0
	WHENCE_CUR = 1
	WHENCE_END = 2
)

const (
	FILESTAT_SET_ATIM     = 0x0001
	FILESTAT_SET_ATIM_NOW = 0x0002
	FILESTAT_SET_MTIM     = 0x0004
	FILESTAT_SET_MTIM_NOW = 0x0008
)

// --- Preopen directory management ---

func init() {
	posix.FDTableInit()

	// Try to set stdio to non-blocking mode before the os package
	// calls NewFile for each fd. For wasip3, SetNonblock is a no-op,
	// but we call it for consistency with wasip1.
	SetNonblock(0, true)
	SetNonblock(1, true)
	SetNonblock(2, true)
}

func init() {
	posix.InitPreopens()

	if posix.Cwd, _ = Getenv("PWD"); posix.Cwd != "" {
		posix.Cwd = joinPath("/", posix.Cwd)
	} else if len(posix.Preopens) > 0 {
		posix.Cwd = posix.Preopens[0].Name
	}
}

// Provided by package runtime.
func now() (sec int64, nsec int32)

// --- File descriptor stat helpers ---

type fdstat struct {
	filetype         filetype
	fdflags          uint16
	rightsBase       rights
	rightsInheriting rights
}

// fd_fdstat_get_flags returns fdflags for a given FD.
func fd_fdstat_get_flags(goFd int) (uint32, error) {
	return posix.FDStatGetFlags(goFd)
}

// fd_fdstat_get_type returns the file type for a given FD.
func fd_fdstat_get_type(goFd int) (uint8, error) {
	return posix.FDStatGetType(goFd)
}

// descriptorTypeToFiletype maps wasip3 descriptor-type values to
// wasip1 FILETYPE_* constants used by stat_wasi.go.
func descriptorTypeToFiletype(dt filesystem.DescriptorType) uint8 {
	return posix.DescriptorTypeToFiletype(dt)
}

// lookupCMDesc returns the CM descriptor handle for a Go FD, or -1 if invalid.
func lookupCMDesc(goFd int) int32 {
	f := posix.Lookup(goFd)
	if f == nil {
		return -1
	}
	return int32(f.Desc)
}

// --- Core filesystem operations ---

func Open(path string, openmode int, perm uint32) (int, error) {
	if path == "" {
		return -1, EINVAL
	}
	of, df := convertFlags(openmode)
	fd, err := posix.FileOpen(path, openmode&O_APPEND != 0, atPathFlags(openmode), of, df)
	return fd, fsErrToSyscall(err)
}

func Openat(dirFd int, path string, openmode int, perm uint32) (int, error) {
	of, df := convertFlags(openmode)
	fd, err := posix.FileOpenAt(dirFd, path, openmode&O_APPEND != 0, atPathFlags(openmode), of, df)
	return fd, fsErrToSyscall(err)
}

func Close(fdNum int) error {
	f := posix.Lookup(fdNum)
	if f == nil {
		return EBADF
	}
	if f.IsSock {
		posix.SockClose(f)
		posix.Free(fdNum)
		return nil
	}
	return fsErrToSyscall(posix.FileClose(fdNum))
}

func CloseOnExec(fdNum int) {
	// nothing to do - no exec
}

func Read(fdNum int, b []byte) (int, error) {
	f := posix.Lookup(fdNum)
	if f == nil {
		return 0, EBADF
	}
	if len(b) == 0 {
		return 0, nil
	}
	if f.IsSock {
		if f.IsUDP {
			n, _, _, _, err := posix.UDPReceive(fdNum, b)
			return n, sockErrToSyscall(err)
		}
		n, err := posix.SockRead(f, b)
		return n, sockErrToSyscall(err)
	}
	n, err := posix.FileRead(f, b, f.Pos)
	if err == nil {
		f.Pos += int64(n)
	}
	return n, fsErrToSyscall(err)
}

func Write(fdNum int, b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if fdNum == 1 || fdNum == 2 {
		n, err := posix.StdioWrite(fdNum, b)
		return n, fsErrToSyscall(err)
	}
	f := posix.Lookup(fdNum)
	if f == nil {
		return 0, EBADF
	}
	if f.IsSock {
		if f.IsUDP {
			err := posix.UDPSendConnected(fdNum, b)
			if err != nil {
				return 0, sockErrToSyscall(err)
			}
			return len(b), nil
		}
		n, err := posix.SockWrite(f, b)
		return n, sockErrToSyscall(err)
	}
	if f.Pos < 0 {
		n, err := posix.FileAppend(f, b)
		return n, fsErrToSyscall(err)
	}
	n, err := posix.FileWrite(f, b, f.Pos)
	if err == nil {
		f.Pos += int64(n)
	}
	return n, fsErrToSyscall(err)
}

// sockErrToSyscall converts fd package errors to syscall errors.
func sockErrToSyscall(err error) error {
	if err == nil {
		return nil
	}
	switch err {
	case posix.ErrBlocked:
		return EAGAIN
	case posix.ErrPipe:
		return EPIPE
	case posix.ErrINVAL:
		return EINVAL
	case posix.ErrMFILE:
		return EMFILE
	case posix.ErrClosed:
		return ECONNABORTED
	}
	if e, ok := err.(posix.SockErrorCode); ok {
		code := int(e)
		if code < len(wasiSocketErrors) && wasiSocketErrors[code] != 0 {
			return wasiSocketErrors[code]
		}
		return Errno(0xffff)
	}
	return err
}

func Pread(fdNum int, b []byte, offset int64) (int, error) {
	f := posix.Lookup(fdNum)
	if f == nil {
		return 0, EBADF
	}
	if len(b) == 0 {
		return 0, nil
	}
	n, err := posix.FileRead(f, b, offset)
	return n, fsErrToSyscall(err)
}

func Pwrite(fdNum int, b []byte, offset int64) (int, error) {
	f := posix.Lookup(fdNum)
	if f == nil {
		return 0, EBADF
	}
	if len(b) == 0 {
		return 0, nil
	}
	n, err := posix.FileWrite(f, b, offset)
	return n, fsErrToSyscall(err)
}

func Seek(fdNum int, offset int64, whence int) (int64, error) {
	f := posix.Lookup(fdNum)
	if f == nil {
		return 0, EBADF
	}
	n, err := posix.FileSeek(f, offset, whence)
	return n, fsErrToSyscall(err)
}

// --- Stat operations ---

type Stat_t struct {
	Dev      uint64
	Ino      uint64
	Filetype uint8
	Nlink    uint64
	Size     uint64
	Atime    uint64
	Mtime    uint64
	Ctime    uint64

	Mode int

	// Uid and Gid are always zero on wasip3 platforms
	Uid uint32
	Gid uint32
}

func Stat(path string, st *Stat_t) error {
	if path == "" {
		return EINVAL
	}
	desc, relPath := posix.PreparePathDesc(path)
	if desc < 0 {
		return EBADF
	}
	ds, err := posix.FileStatAt(desc, int32(filesystem.PathFlagsSymlinkFollow), relPath)
	if err != nil {
		return fsErrToSyscall(err)
	}
	parseDescriptorStat(ds, st)
	return nil
}

func Lstat(path string, st *Stat_t) error {
	if path == "" {
		return EINVAL
	}
	desc, relPath := posix.PreparePathDesc(path)
	if desc < 0 {
		return EBADF
	}
	ds, err := posix.FileStatAt(desc, 0, relPath)
	if err != nil {
		return fsErrToSyscall(err)
	}
	parseDescriptorStat(ds, st)
	return nil
}

func Fstat(fdNum int, st *Stat_t) error {
	if fdNum >= 0 && fdNum <= 2 {
		// Stdio fds are wasi:cli streams, not filesystem descriptors.
		// Report them as character devices.
		*st = Stat_t{Filetype: FILETYPE_CHARACTER_DEVICE, Nlink: 1}
		setDefaultMode(st)
		return nil
	}
	f := posix.Lookup(fdNum)
	if f == nil {
		return EBADF
	}
	ds, err := posix.FileStat(f)
	if err != nil {
		return fsErrToSyscall(err)
	}
	parseDescriptorStat(ds, st)
	return nil
}

// parseDescriptorStat converts a filesystem.DescriptorStatT into a Go Stat_t.
func parseDescriptorStat(stat filesystem.DescriptorStatT, st *Stat_t) {
	st.Dev = 0
	st.Ino = 0
	st.Filetype = descriptorTypeToFiletype(stat.Type)
	st.Nlink = uint64(stat.LinkCount)
	st.Size = uint64(stat.Size)

	if stat.DataAccessTimestamp != nil {
		st.Atime = uint64(stat.DataAccessTimestamp.Seconds*1e9) + uint64(stat.DataAccessTimestamp.Nanoseconds)
	} else {
		st.Atime = 0
	}

	if stat.DataModificationTimestamp != nil {
		st.Mtime = uint64(stat.DataModificationTimestamp.Seconds*1e9) + uint64(stat.DataModificationTimestamp.Nanoseconds)
	} else {
		st.Mtime = 0
	}

	if stat.StatusChangeTimestamp != nil {
		st.Ctime = uint64(stat.StatusChangeTimestamp.Seconds*1e9) + uint64(stat.StatusChangeTimestamp.Nanoseconds)
	} else {
		st.Ctime = 0
	}

	setDefaultMode(st)
}

func setDefaultMode(st *Stat_t) {
	if st.Filetype == FILETYPE_DIRECTORY {
		st.Mode = 0700
	} else {
		st.Mode = 0600
	}
}

// --- Directory operations ---

func Mkdir(path string, perm uint32) error {
	if path == "" {
		return EINVAL
	}
	desc, relPath := posix.PreparePathDesc(path)
	if desc < 0 {
		return EBADF
	}
	return fsErrToSyscall(posix.CreateDirAt(desc, relPath))
}

func ReadDir(fdNum int, buf []byte, cookie dircookie) (int, error) {
	// Not used on wasip3 — the os package calls posix.ReadDirEntries directly.
	return 0, ENOSYS
}

// --- Unlink / Rmdir ---

func Unlink(path string) error {
	if path == "" {
		return EINVAL
	}
	desc, relPath := posix.PreparePathDesc(path)
	if desc < 0 {
		return EBADF
	}
	return fsErrToSyscall(posix.UnlinkFileAt(desc, relPath))
}

func Rmdir(path string) error {
	if path == "" {
		return EINVAL
	}
	desc, relPath := posix.PreparePathDesc(path)
	if desc < 0 {
		return EBADF
	}
	return fsErrToSyscall(posix.RemoveDirAt(desc, relPath))
}

// --- Chmod / Chown ---

// Chmod is a no-op on WASI (no permission model). Stat is called to verify
// the path exists, returning an error for non-existent paths.
func Chmod(path string, mode uint32) error {
	var stat Stat_t
	return Stat(path, &stat)
}

// Fchmod is a no-op on WASI (no permission model). Fstat is called to verify
// the fd is valid, returning an error for bad descriptors.
func Fchmod(fdNum int, mode uint32) error {
	var stat Stat_t
	return Fstat(fdNum, &stat)
}

func Chown(path string, uid, gid int) error {
	return ENOSYS
}

func Fchown(fdNum int, uid, gid int) error {
	return ENOSYS
}

func Lchown(path string, uid, gid int) error {
	return ENOSYS
}

// --- UtimesNano ---

func UtimesNano(path string, ts []Timespec) error {
	if path == "" {
		return EINVAL
	}
	desc, relPath := posix.PreparePathDesc(path)
	if desc < 0 {
		return EBADF
	}
	atime, mtime := convertTimestamps(ts[0], ts[1])
	return fsErrToSyscall(posix.SetTimesAt(desc, int32(filesystem.PathFlagsSymlinkFollow), relPath, atime, mtime))
}

// --- Rename ---

func Rename(from, to string) error {
	if from == "" || to == "" {
		return EINVAL
	}
	oldDesc, oldPath := posix.PreparePathDesc(from)
	newDesc, newPath := posix.PreparePathDesc(to)
	if oldDesc < 0 || newDesc < 0 {
		return EBADF
	}
	return fsErrToSyscall(posix.RenameAt(oldDesc, oldPath, newDesc, newPath))
}

// --- Truncate / Ftruncate ---

func Truncate(path string, length int64) error {
	if path == "" {
		return EINVAL
	}
	fdNum, err := Open(path, O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer Close(fdNum)
	return Ftruncate(fdNum, length)
}

func Ftruncate(fdNum int, length int64) error {
	f := posix.Lookup(fdNum)
	if f == nil {
		return EBADF
	}
	return fsErrToSyscall(posix.FileSetSize(f, length))
}

// --- Getwd / Chdir ---

const ImplementsGetwd = true

func Getwd() (string, error) {
	return posix.Cwd, nil
}

func Chdir(path string) error {
	if path == "" {
		return EINVAL
	}

	dir := "/"
	if !isAbs(path) {
		dir = posix.Cwd
	}
	path = joinPath(dir, path)

	var st Stat_t
	if err := Stat(path, &st); err != nil {
		return err
	}
	if st.Filetype != FILETYPE_DIRECTORY {
		return ENOTDIR
	}
	posix.Cwd = path
	return nil
}

// --- Readlink ---

func Readlink(path string, buf []byte) (n int, err error) {
	if path == "" {
		return 0, EINVAL
	}
	if len(buf) == 0 {
		return 0, nil
	}
	desc, relPath := posix.PreparePathDesc(path)
	if desc < 0 {
		return 0, EBADF
	}
	target, linkErr := posix.ReadlinkAt(desc, relPath)
	if linkErr != nil {
		return 0, fsErrToSyscall(linkErr)
	}
	n = copy(buf, target)
	return n, nil
}

// --- Link ---

func Link(path, link string) error {
	if path == "" || link == "" {
		return EINVAL
	}
	oldDesc, oldPath := posix.PreparePathDesc(path)
	newDesc, newPath := posix.PreparePathDesc(link)
	if oldDesc < 0 || newDesc < 0 {
		return EBADF
	}
	return fsErrToSyscall(posix.LinkAt(oldDesc, oldPath, newDesc, newPath, 0))
}

// --- Symlink ---

func Symlink(path, link string) error {
	if path == "" || link == "" {
		return EINVAL
	}
	desc, newPath := posix.PreparePathDesc(link)
	if desc < 0 {
		return EBADF
	}
	return fsErrToSyscall(posix.SymlinkAt(path, desc, newPath))
}

// --- Fsync ---

func Fsync(fdNum int) error {
	f := posix.Lookup(fdNum)
	if f == nil {
		return EBADF
	}
	return fsErrToSyscall(posix.FileSync(f))
}

func Dup(fdNum int) (int, error) {
	return 0, ENOSYS
}

func Dup2(fdNum, newfd int) error {
	return ENOSYS
}

func Pipe(fds []int) error {
	return ENOSYS
}

// --- At-functions (used by internal/syscall/unix and net via direct import) ---

func Unlinkat(dirfd int, path string, flags int) error {
	f := posix.Lookup(dirfd)
	if f == nil {
		return EBADF
	}
	const atRemovedir = 0x200
	if flags&atRemovedir == 0 {
		return fsErrToSyscall(posix.UnlinkFileAt(f.Desc, path))
	}
	return fsErrToSyscall(posix.RemoveDirAt(f.Desc, path))
}

func Fstatat(dirfd int, path string, stat *Stat_t, flags int) error {
	f := posix.Lookup(dirfd)
	if f == nil {
		return EBADF
	}
	ds, err := posix.FileStatAt(f.Desc, atPathFlags(flags), path)
	if err != nil {
		return fsErrToSyscall(err)
	}
	parseDescriptorStat(ds, stat)
	return nil
}

func Readlinkat(dirfd int, path string, buf []byte) (int, error) {
	f := posix.Lookup(dirfd)
	if f == nil {
		return 0, EBADF
	}
	target, err := posix.ReadlinkAt(f.Desc, path)
	if err != nil {
		return 0, fsErrToSyscall(err)
	}
	n := copy(buf, target)
	return n, nil
}

func Mkdirat(dirfd int, path string, mode uint32) error {
	if path == "" {
		return EINVAL
	}
	f := posix.Lookup(dirfd)
	if f == nil {
		return EBADF
	}
	return fsErrToSyscall(posix.CreateDirAt(f.Desc, path))
}

func Renameat(olddirfd int, oldpath string, newdirfd int, newpath string) error {
	if oldpath == "" || newpath == "" {
		return EINVAL
	}
	oldF := posix.Lookup(olddirfd)
	newF := posix.Lookup(newdirfd)
	if oldF == nil || newF == nil {
		return EBADF
	}
	return fsErrToSyscall(posix.RenameAt(oldF.Desc, oldpath, newF.Desc, newpath))
}

func Linkat(olddirfd int, oldpath string, newdirfd int, newpath string, flag int) error {
	if oldpath == "" || newpath == "" {
		return EINVAL
	}
	oldF := posix.Lookup(olddirfd)
	newF := posix.Lookup(newdirfd)
	if oldF == nil || newF == nil {
		return EBADF
	}
	return fsErrToSyscall(posix.LinkAt(oldF.Desc, oldpath, newF.Desc, newpath, 0))
}

func Symlinkat(oldpath string, newdirfd int, newpath string) error {
	if oldpath == "" || newpath == "" {
		return EINVAL
	}
	f := posix.Lookup(newdirfd)
	if f == nil {
		return EBADF
	}
	return fsErrToSyscall(posix.SymlinkAt(oldpath, f.Desc, newpath))
}

func Utimensat(dirfd int, path string, times *[2]Timespec, flag int) error {
	if path == "" {
		return EINVAL
	}
	f := posix.Lookup(dirfd)
	if f == nil {
		return EBADF
	}
	atime, mtime := convertTimestamps(times[0], times[1])
	return fsErrToSyscall(posix.SetTimesAt(f.Desc, atPathFlags(flag), path, atime, mtime))
}
