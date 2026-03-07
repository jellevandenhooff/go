// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package componentize

import "fmt"

// appendUleb128 appends the unsigned LEB128 encoding of v to b.
func appendUleb128(b []byte, v uint32) []byte {
	for {
		c := uint8(v & 0x7f)
		v >>= 7
		if v != 0 {
			c |= 0x80
		}
		b = append(b, c)
		if v == 0 {
			break
		}
	}
	return b
}

// uleb128Size returns the number of bytes needed to encode v as unsigned LEB128.
func uleb128Size(v uint32) int {
	size := 1
	for v >>= 7; v != 0; v >>= 7 {
		size++
	}
	return size
}

// readUleb128 reads an unsigned LEB128 value from data at offset.
// Returns the value and the new offset after the encoded bytes.
func readUleb128(data []byte, offset int) (uint32, int, error) {
	var result uint32
	var shift uint
	for i := 0; ; i++ {
		if offset >= len(data) {
			return 0, 0, fmt.Errorf("unexpected end of data reading uleb128 at offset %d", offset)
		}
		b := data[offset]
		offset++
		result |= uint32(b&0x7f) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
		if shift >= 35 {
			return 0, 0, fmt.Errorf("uleb128 overflow at offset %d", offset)
		}
	}
	return result, offset, nil
}

// appendName appends a wasm name (LEB128-prefixed UTF-8 string) to b.
func appendName(b []byte, name string) []byte {
	b = appendUleb128(b, uint32(len(name)))
	b = append(b, name...)
	return b
}

// readName reads a wasm name (LEB128-prefixed UTF-8 string) from data at offset.
func readName(data []byte, offset int) (string, int, error) {
	nameLen, offset, err := readUleb128(data, offset)
	if err != nil {
		return "", 0, err
	}
	end := offset + int(nameLen)
	if end > len(data) {
		return "", 0, fmt.Errorf("name length %d exceeds data at offset %d", nameLen, offset)
	}
	return string(data[offset:end]), end, nil
}
