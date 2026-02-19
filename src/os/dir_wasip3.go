// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package os

import (
	"internal/wasi/posix"
	"io"
	"sync"
)

// dirInfo holds the state for an in-progress directory read.
// On wasip3, each call to ReadDirEntries returns all entries at once,
// so we cache the full list and serve subsequent calls from it.
type dirInfo struct {
	mu      sync.Mutex
	entries []posix.DirEntry
	pos     int
	done    bool
}

func (d *dirInfo) close() {
	d.entries = nil
	d.pos = 0
	d.done = false
}

func (f *File) readdir(n int, mode readdirMode) (names []string, dirents []DirEntry, infos []FileInfo, err error) {
	var d *dirInfo
	for {
		d = f.dirinfo.Load()
		if d != nil {
			break
		}
		newD := new(dirInfo)
		if f.dirinfo.CompareAndSwap(nil, newD) {
			d = newD
			break
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// Fetch entries if we haven't yet.
	if d.entries == nil && !d.done {
		d.entries, err = posix.ReadDirEntries(int(f.pfd.Sysfd))
		if err != nil {
			return nil, nil, nil, &PathError{Op: "readdir", Path: f.name, Err: err}
		}
		// Filter out . and .. entries.
		filtered := d.entries[:0]
		for _, e := range d.entries {
			if e.Name != "." && e.Name != ".." {
				filtered = append(filtered, e)
			}
		}
		d.entries = filtered
		d.done = true
	}

	// Change the meaning of n: negative means read all, positive means bounded.
	if n == 0 {
		n = -1
	}

	for n != 0 && d.pos < len(d.entries) {
		e := d.entries[d.pos]
		d.pos++
		if n > 0 {
			n--
		}
		typ := descriptorTypeToFileMode(e.Type)
		switch mode {
		case readdirName:
			names = append(names, e.Name)
		case readdirDirEntry:
			de, err := newUnixDirent(f.name, e.Name, typ)
			if IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, dirents, nil, err
			}
			dirents = append(dirents, de)
		case readdirFileInfo:
			info, err := lstat(f.name + "/" + e.Name)
			if IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, nil, infos, err
			}
			infos = append(infos, info)
		}
	}

	if n > 0 && len(names)+len(dirents)+len(infos) == 0 {
		return nil, nil, nil, io.EOF
	}
	return names, dirents, infos, nil
}

// descriptorTypeToFileMode converts a WASI 0.3 descriptor-type enum
// to an os.FileMode type bits value.
func descriptorTypeToFileMode(dt uint8) FileMode {
	switch dt {
	case 1: // block-device
		return ModeDevice
	case 2: // character-device
		return ModeDevice | ModeCharDevice
	case 3: // directory
		return ModeDir
	case 4: // fifo
		return ModeNamedPipe
	case 5: // symbolic-link
		return ModeSymlink
	case 6: // regular-file
		return 0
	case 7: // socket
		return ModeSocket
	default: // unknown
		return ^FileMode(0)
	}
}
