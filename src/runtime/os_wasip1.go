// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasip1

package runtime

import (
	"structs"
	"unsafe"
)

type eventtype = uint8

const (
	eventtypeClock eventtype = iota
	eventtypeFdRead
	eventtypeFdWrite
)

type eventrwflags = uint16

const (
	fdReadwriteHangup eventrwflags = 1 << iota
)

type userdata = uint64

// The go:wasmimport directive currently does not accept values of type uint16
// in arguments or returns of the function signature. Most WASI imports return
// an errno value, which we have to define as uint32 because of that limitation.
// However, the WASI errno type is intended to be a 16 bits integer, and in the
// event struct the error field should be of type errno. If we used the errno
// type for the error field it would result in a mismatching field alignment and
// struct size because errno is declared as a 32 bits type, so we declare the
// error field as a plain uint16.
type event struct {
	_           structs.HostLayout
	userdata    userdata
	error       uint16
	typ         eventtype
	fdReadwrite eventFdReadwrite
}

type eventFdReadwrite struct {
	_      structs.HostLayout
	nbytes filesize
	flags  eventrwflags
}

type subclockflags = uint16

const (
	subscriptionClockAbstime subclockflags = 1 << iota
)

type subscriptionClock struct {
	_         structs.HostLayout
	id        clockid
	timeout   timestamp
	precision timestamp
	flags     subclockflags
}

type subscriptionFdReadwrite struct {
	_  structs.HostLayout
	fd int32
}

type subscription struct {
	_        structs.HostLayout
	userdata userdata
	u        subscriptionUnion
}

type subscriptionUnion [5]uint64

func (u *subscriptionUnion) eventtype() *eventtype {
	return (*eventtype)(unsafe.Pointer(&u[0]))
}

func (u *subscriptionUnion) subscriptionClock() *subscriptionClock {
	return (*subscriptionClock)(unsafe.Pointer(&u[1]))
}

func (u *subscriptionUnion) subscriptionFdReadwrite() *subscriptionFdReadwrite {
	return (*subscriptionFdReadwrite)(unsafe.Pointer(&u[1]))
}

//go:wasmimport wasi_snapshot_preview1 proc_exit
func exit(code int32)

//go:wasmimport wasi_snapshot_preview1 poll_oneoff
//go:noescape
func poll_oneoff(in *subscription, out *event, nsubscriptions size, nevents *size) errno

func usleep(usec uint32) {
	var in subscription
	var out event
	var nevents size

	eventtype := in.u.eventtype()
	*eventtype = eventtypeClock

	subscription := in.u.subscriptionClock()
	subscription.id = clockMonotonic
	subscription.timeout = timestamp(usec) * 1e3
	subscription.precision = 1e3

	if poll_oneoff(&in, &out, 1, &nevents) != 0 {
		throw("wasi_snapshot_preview1.poll_oneoff")
	}
}
