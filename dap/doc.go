// Package dap manages an Arm Debug Access Port over SWD.
//
// A DebugPort owns the volatile debug-port state it acquires with Connect. A
// MemAP owns the access-port register values it changes. Release them in
// reverse order: the MemAP first, then the DebugPort.
//
// DebugPort and MemAP values are not safe for concurrent use. Serialize calls
// that share either value or the underlying swd.Conn. A DebugPort requires
// exclusive use of that SWD transaction stream until it is no longer used;
// direct transfers can invalidate its cached register selection and response
// state.
//
// A failed Connect attempts bounded cleanup before returning. If that cleanup
// also fails, Release remains available but other debug-port and access-port
// operations fail until cleanup succeeds. A failed Release has the same
// cleanup-only behavior and may be retried.
package dap
