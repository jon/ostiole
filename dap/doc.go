// Package dap manages an Arm Debug Access Port over SWD or baseline ADIv5 JTAG-DP.
//
// Construct a Port with SWDP or JTAGDP and pass it to NewDebugPort. These
// constructors send no traffic; Connect validates the binding, enters the
// protocol, and acquires volatile debug-port state. JTAG requires a complete
// explicit chain and zero-based, TDO-first TAP index with a four- or eight-bit IR.
// OpenMemAP validates one access port and snapshots the register values it will
// change. Release them in reverse order: the MemAP first, then the DebugPort.
//
// SWD retries a clean WAIT on the rejected physical request; JTAG polls the
// preceding accepted request without replay. Both stop at the configured WAIT
// limit or when the context ends. NewDebugPort uses only the context unless
// WithMaxWaits supplies a response-count limit. SetMaxWaits can change it
// while the port is idle. If the context ends, errors.Is reports its error;
// the original WAIT is not retained as ErrWait. An independently joined
// cleanup failure remains visible. A FAULT ends the operation. An accepted
// write is not replayed; if its RDBUFF completion request returns WAIT, only
// that completion is polled. Identity distinguishes a decoded DPIDR on SWD
// from a raw IDCODE on JTAG; neither accessor fabricates the other.
//
// MemAP.ReadScalar and MemAP.WriteScalar perform aligned scalar target-memory
// accesses; MemAP.ReadBlock and MemAP.WriteBlock accept arbitrary byte ranges.
// If single address increment is unavailable, block access writes TAR before
// each word. Writes change target memory at the addresses selected by their
// callers. The package checks scalar value width, alignment, and advertised
// MEM-AP extensions, not whether an address is safe to modify. If a failed
// Size64 transfer might have started its first DRW access, release the MemAP
// and DebugPort before reconnecting.
//
// DebugPort and MemAP values are not safe for concurrent use. Serialize calls
// that share either value or the underlying SWD connection or JTAG chain.
// A DebugPort requires exclusive use until Release succeeds;
// direct transfers can invalidate its cached register selection and response
// state.
//
// ReadDP and WriteDP accept logical debug-port register names. They distinguish
// operations which share a physical offset and enforce direction and availability.
// SWD manages DPBANKSEL without exposing a current-bank operation. Baseline
// JTAG supports readable SELECT, rejects banked registers and DPIDR, and accepts
// only the architectural DAPABORT value for ABORT. Later JTAG-DP versions and
// version detection are not implemented.
//
// NewAPSel constructs an ADIv5 index selector; APAt constructs an ADIv6
// base-address selector.
// The zero APSel is invalid. APSel.Address combines a selector with a complete
// register offset (eight bits for ADIv5, twelve for ADIv6). The resulting
// APAddress also has an invalid zero value. ReadAPIDR reads and
// decodes the common read-only AP identity. ReadRawAP and WriteRawAP reject
// invalid or unaligned addresses before traffic. Raw access has the effects
// defined by the selected AP class; writing a MEM-AP data register can write
// target memory. A raw access which completes or might have completed
// invalidates existing MemAP values.
//
// EnumerateAPs scans every ADIv5 AP selector and reports each nonzero identity.
// It does not read class-specific registers.
//
// Public DP, AP, transaction, and MEM-AP operations require a successful
// Connect. The underlying SWD connection establishes the simple or fixed
// response grammar, tries to enable ORUNDETECT, and restores that change during
// Release. JTAG temporarily disables inherited ORUNDETECT and restores it on
// release. It rejects active pushed-operation and transaction-counter modes.
// DAP writes must preserve the binding-owned mode.
//
// A failed Connect attempts bounded cleanup before returning. If that cleanup
// also fails, Release remains available but other debug-port and access-port
// operations fail until cleanup succeeds. A failed Release has the same
// cleanup-only behavior and may be retried. WithCleanupTimeout configures each
// independent recovery attempt: one second by default for SWD, thirty for JTAG.
// JTAG recovery revalidates the exact chain and reacquires the TAP before
// restoration. The debug port never closes or automatically reopens the probe.
//
// A DebugPort does not replay a request which returns FAULT. It reads
// bank-zero CTRL/STAT when the register selection is known, clears the sticky
// conditions reported there, verifies the clear, and returns a FaultError. A
// SWD SELECT write remains provisional until later traffic establishes whether
// its data took effect. Failed FAULT cleanup leaves the port in the same
// cleanup-only state as a failed release.
//
// A Txn queues an ordered group of DP and AP operations. Commit validates the
// complete queue, settles any earlier immediate DP write, then sends queued
// traffic through a private SWD or JTAG executor. SWD retains its packed frames;
// JTAG executes logical operations sequentially and checks CTRL/STAT after each
// AP operation, because its acknowledgement combines OK and FAULT.
// ReadResult.Value reports data from a queued read; WriteResult.Err reports
// completion of a queued write. DP writes and AP operations settle
// through RDBUFF. If an operation fails, earlier confirmed results remain
// available and later operations report that they were not executed. A result
// reports ErrIndeterminate when traffic was clocked but completion cannot be
// established.
package dap
