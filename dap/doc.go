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
//
// A DebugPort does not replay a request which returns FAULT. It reads
// bank-zero CTRL/STAT when the register selection is known, clears the sticky
// conditions reported there, verifies the clear, and returns a FaultError. A
// SELECT write remains provisional until later traffic establishes whether
// its data took effect. Failed FAULT cleanup leaves the port in the same
// cleanup-only state as a failed release.
//
// A Txn queues an ordered group of DP and AP operations. Commit validates the
// complete queue, settles any earlier raw DP write, then sends queued traffic.
// DP writes and AP operations settle through RDBUFF. If an operation fails,
// earlier confirmed results remain available and later operations report that
// they were not executed. If traffic was clocked but completion cannot be
// established, the Result reports ErrIndeterminate.
package dap
