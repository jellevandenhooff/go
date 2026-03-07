// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip3

package exec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound is the error resulting if a path search failed to find an executable file.
var ErrNotFound = errors.New("executable file not found in $PATH")

func findExecutable(file string) error {
	d, err := os.Stat(file)
	if err != nil {
		return err
	}
	if d.IsDir() {
		return errors.New("is a directory")
	}
	return nil
}

// lookPath searches for an executable named file in the
// directories named by the PATH environment variable.
// On wasip3, executables are .wasm files and don't need execute permission.
func lookPath(file string) (string, error) {
	if strings.Contains(file, "/") {
		err := findExecutable(file)
		if err == nil {
			return file, nil
		}
		return "", &Error{file, err}
	}
	path := os.Getenv("PATH")
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, file)
		if err := findExecutable(p); err == nil {
			return p, nil
		}
	}
	return "", &Error{file, ErrNotFound}
}

// lookExtensions is a no-op on non-Windows platforms.
func lookExtensions(path, dir string) (string, error) {
	return path, nil
}
