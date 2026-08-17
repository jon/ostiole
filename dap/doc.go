// Package dap manages an Arm Debug Access Port over SWD.
//
// A DebugPort enters SWD and acquires volatile debug-port state with Connect.
// OpenMemAP validates one access port and snapshots the register values it will
// change. Release them in reverse order: the MemAP first, then the DebugPort.
//
// DebugPort and MemAP values are not safe for concurrent use. Serialize calls
// that share either value or the underlying swd.Conn. A DebugPort requires
// exclusive use of that SWD transaction stream until it is no longer used;
// direct transfers can invalidate its cached register selection and response
// state.
//
// ReadDP and WriteDP accept logical ADIv5 register names. They distinguish
// operations which share a physical SWD offset, enforce direction, and manage
// DPBANKSEL without exposing a current-bank operation.
//
// NewAPSel constructs an access-port selector; the zero APSel is invalid.
// APSel.Address combines a selector with a complete eight-bit ADIv5 AP address;
// the resulting APAddress also has an invalid zero value. ReadAPIDR reads and
// decodes the common read-only AP identity. ReadRawAP and WriteRawAP reject
// invalid or unaligned addresses before traffic. Raw access has the effects
// defined by the selected AP class; writing a MEM-AP data register can write
// target memory. A raw access which completes or might have completed
// invalidates existing MemAP values.
//
// Public DP, AP, transaction, and MEM-AP operations require a successful
// Connect.
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
// complete queue, settles any earlier immediate DP write, then sends queued
// traffic.
// DP writes and AP operations settle through RDBUFF. If an operation fails,
// earlier confirmed results remain available and later operations report that
// they were not executed. If traffic was clocked but completion cannot be
// established, the Result reports ErrIndeterminate.
package dap
