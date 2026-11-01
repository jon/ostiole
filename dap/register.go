package dap

import (
	"context"
	"reflect"
)

// RegisterStatus describes the physical outcome of one register operation.
// Its zero value is invalid.
type RegisterStatus uint8

const (
	// RegisterCompleted reports an operation whose completion is known.
	RegisterCompleted RegisterStatus = iota + 1
	// RegisterRejected reports a request rejected before its operation started.
	RegisterRejected
	// RegisterUnsent reports a request which was not sent.
	RegisterUnsent
	// RegisterIndeterminate reports an operation which may have taken effect
	// without confirmed completion. It must not be replayed.
	RegisterIndeterminate
)

// RegisterTransport supplies completed SW-DP register operations. Addresses
// are physical register offsets 0, 4, 8 or 12; DebugPort owns logical selection.
// AP reads return completed values, and successful writes include completion.
// Rejected WAIT includes response-mode cleanup and matches ErrWait.
// Additional failures stop retry; failed completion is indeterminate.
// FAULT errors match ErrFault.
// Every operation reports its outcome, including errors and cancellation.
// Completed reads retain their value on cancellation after completion; bad
// parity and FAULT do not confirm read data. Accepted writes with failed
// completion report RegisterIndeterminate and must not be replayed.
// An operation which may have started must never be reported as unsent or
// rejected. A zero or inconsistent status is a protocol error.
//
// Connect enters SWD, clears supported sticky state, establishes bank zero
// and returns DPIDR. The transport owns response framing and its ORUNDETECT
// change. Repeated Connect repairs framing while retaining the original
// restoration snapshot. Release restores that snapshot, leaves SWD selected,
// and never closes host resources. CleanupPending reports without traffic
// whether protocol restoration remains outstanding, including failed entry.
// Connect and Release retain failed restoration for retry with a fresh context.
//
// Calls must be serialized. Started operations finish under the transport's
// bounded completion budget; DebugPort owns WAIT policy and logical recovery.
type RegisterTransport interface {
	Connect(context.Context) (uint32, error)
	Release(context.Context) error
	CleanupPending() bool
	ReadDP(context.Context, uint8) (uint32, RegisterStatus, error)
	WriteDP(context.Context, uint8, uint32) (RegisterStatus, error)
	ReadAP(context.Context, uint8) (uint32, RegisterStatus, error)
	WriteAP(context.Context, uint8, uint32) (RegisterStatus, error)
}

// RegisterDP binds a non-nil completed-register SW-DP transport without traffic.
// Typed nil transports are invalid.
// Give DebugPort exclusive use until Release succeeds. Existing wire
// compositions continue to use SWDP(swd.New(wire)); completed transports use
// RegisterDP(transport). Neither constructor enters the protocol.
func RegisterDP(transport RegisterTransport) Port {
	value := reflect.ValueOf(transport)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return Port{}
		}
	}
	return Port{register: transport}
}
