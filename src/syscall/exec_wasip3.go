// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3 && goexperiment.wasiexec

package syscall

import (
	"internal/wasi"
	"internal/wasi/posix"
)

// Process result storage. Since wasm is single-threaded, a simple map suffices.
// StartProcess stores results here; Wait4 retrieves them.
var (
	nextPid     int = 1000
	procResults     = make(map[int]*procResult)
	execTmpDir  string
	execCounter int
)

type procResult struct {
	exitCode uint32
}

func execTempPath(suffix string) string {
	if execTmpDir == "" {
		execTmpDir, _ = Getenv("TMPDIR")
		if execTmpDir == "" {
			execTmpDir = "/tmp"
		}
	}
	execCounter++
	return execTmpDir + "/goexec-" + itoa(execCounter) + suffix
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// StartProcess starts a child WASI component by calling the host exec API.
// On wasip3, this runs the child synchronously — the calling goroutine is
// suspended (via async import) while the child executes.
//
// The child's stdout/stderr are captured via temp files and written into the
// pipe file descriptors passed in attr.Files[1] and attr.Files[2].
func StartProcess(argv0 string, argv []string, attr *ProcAttr) (pid int, handle uintptr, err error) {
	env := attr.Env
	cwd := attr.Dir

	// Create temp file paths for stdout/stderr capture
	stdoutPath := execTempPath("-stdout")
	stderrPath := execTempPath("-stderr")

	// Call the WASI exec import
	result, execErr := wasi.Exec(argv0, argv, env, cwd, stdoutPath, stderrPath)
	if execErr != nil {
		return 0, 0, ENOENT
	}

	// Read captured stdout from temp file and write to pipe
	if len(attr.Files) > 1 {
		stdoutFd := int(attr.Files[1])
		f := posix.Lookup(stdoutFd)
		if f != nil && f.IsPipe && f.PipeWrite != nil {
			data := readAndRemoveFile(stdoutPath)
			if len(data) > 0 {
				f.PipeWrite.Write(data)
			}
		}
	}

	// Read captured stderr from temp file and write to pipe
	if len(attr.Files) > 2 {
		stderrFd := int(attr.Files[2])
		f := posix.Lookup(stderrFd)
		if f != nil && f.IsPipe && f.PipeWrite != nil {
			data := readAndRemoveFile(stderrPath)
			if len(data) > 0 {
				f.PipeWrite.Write(data)
			}
		}
	}

	// Assign a fake pid and store the result for Wait4
	pid = nextPid
	nextPid++
	procResults[pid] = &procResult{exitCode: result.ExitCode}

	return pid, 0, nil
}

// readAndRemoveFile reads a file's contents and removes it.
func readAndRemoveFile(path string) []byte {
	fd, err := Open(path, O_RDONLY, 0)
	if err != nil {
		return nil
	}
	defer Close(fd)
	defer Unlink(path)

	// Read in chunks
	var data []byte
	buf := make([]byte, 32768)
	for {
		n, err := Read(fd, buf)
		if n > 0 {
			data = append(data, buf[:n]...)
		}
		if err != nil || n == 0 {
			break
		}
	}
	return data
}

func Wait4(pid int, wstatus *WaitStatus, options int, rusage *Rusage) (wpid int, err error) {
	result, ok := procResults[pid]
	if !ok {
		return 0, ECHILD
	}
	delete(procResults, pid)

	if wstatus != nil {
		// Encode exit status in the WaitStatus format.
		// On Unix, WaitStatus encodes the exit code in bits 8-15
		// when the process exited normally (bits 0-7 = 0).
		*wstatus = WaitStatus(result.exitCode << 8)
	}

	return pid, nil
}
