# Capabilities

This guide describes the behavior implemented by the current public packages.
It distinguishes an API from the environments where that API is exercised in
CI or on physical hardware.

“Implemented” means the behavior is present and covered by ordinary tests.
“Simulated” means a public behavioral model exercises the same package
boundary. “HIL” means an opt-in integration test has exercised physical
hardware. None of these labels imply support for every device in a product
family or every feature of a protocol.

## Host USB

| Capability | Implemented | Validation and boundary |
| --- | --- | --- |
| Linux host access | Yes | Pure-Go sysfs inventory and usbfs transfers; Linux CI and FT232H HIL. Permission setup and manual release of a bound kernel driver are host prerequisites. |
| macOS host access | Yes | IOKit and IOUSBLib through cgo; macOS 26 arm64 and Intel CI with a macOS 12 deployment target. |
| Filtered enumeration | Yes | Explicit exact-product and vendor-only filters, including an exact product ID of zero; deterministic bus/address ordering and context checks. |
| Exact open | Yes | Revalidates bus, address, vendor, and product before and after opening, then retains that identity on the device. |
| Interface ownership | Yes | `ClaimedInterface` selects alternate settings and releases the interface; a failed release can be retried. `Device.Close` waits for that release. Linux reports contention rather than detaching a bound kernel driver. |
| Control transfers | Yes | Synchronous, deadline-bounded endpoint-zero transfers. |
| Bulk transfers | Yes | Synchronous, deadline-bounded bulk IN and OUT transfers. |
| Linux FT232H ownership | HIL | Manual `ftdi_sio` unbind, unprivileged usbfs claim and MPSSE/SWD traffic, release, and explicit driver rebind. |
| macOS FT232H ownership | HIL | Interface seizure, control/bulk traffic, MPSSE setup, close, and Apple driver rematch. |

The USB package does not currently expose descriptor trees, device strings,
hotplug events, multiple simultaneous interface claims, interrupt or
isochronous transfers, device reset, or configuration switching.

Linux is the only pure-Go host. macOS builds require cgo and the Xcode or
Xcode command-line-tool SDK; they do not require libusb or another installed
USB library. Windows is not supported. See [Linux USB access](linux-usb.md)
for the host setup required by physical Linux USB operations.

## FTDI MPSSE

| Capability | Implemented | Validation and boundary |
| --- | --- | --- |
| FT232H | Yes | Port A; full MPSSE and SWD HIL on Linux and macOS. |
| FT2232H | Yes | Ports A and B using the standard H-series interface and endpoint layout. |
| FT4232H | Yes | Ports A and B using the standard H-series interface and endpoint layout. |
| Explicit clock | Yes | `MaxClockHz` is a ceiling; `Channel.ClockHz` reports the attainable configured rate. Examples request 400 kHz. |
| MPSSE lifecycle | Yes | Claim, reset, synchronize, configure pins/clock, reset bit mode, set the latency timer to 16 ms, purge the receive and transmit paths, release, and close. |
| SWD bit streams | Yes | Direction-safe output and input runs with exact bulk exchange. |
| JTAG | No | No public JTAG engine or FTDI JTAG interface exists. |

The driver binds the standard FTDI H-series interfaces and endpoint numbers.
It does not inspect USB descriptors to verify a different layout. A listed
USB identity is a candidate, not evidence that every board using that identity
wires its MPSSE port for debugging.

## Serial Wire Debug

| Capability | Implemented | Validation and boundary |
| --- | --- | --- |
| Line reset | Yes | The wire sequence and simulated STICKYORUN and DLCR effects are unit tested. |
| JTAG-to-SWD selection | Yes | Unit tested and used by every SWD HIL path. |
| Connection lifecycle | Yes | `Connect` validates DPIDR before configuration, establishes bank zero and CTRL/STAT framing, and tries to enable ORUNDETECT. It uses overrun framing only if the bit reads back as set. `Release` restores the inherited setting and can be retried after failure. Register access remains blocked before Connect and after Release. |
| DP and AP requests | Yes | Separate `ReadDP`, `WriteDP`, `ReadAP`, and `WriteAP` calls validate the physical register address before sending one request with header, turnaround, data, and idle cycles. |
| ACK classification | Yes | OK, WAIT, FAULT, and invalid acknowledgements are distinguished. |
| Overrun response framing | Yes | A connected target with ORUNDETECT set uses one fixed request, acknowledgement, data, turnaround, and idle frame. WAIT and FAULT include the data phase. |
| Read parity | Yes | Invalid read parity is reported. |
| Automatic retries | No | A raw register call does not replay the requested transaction. In overrun mode it clears STICKYORUN before returning WAIT; retry policy belongs to the caller. |
| Ordered raw queue | Yes | `swd.Batch` validates all queued DP/AP operations before traffic, sends them in order, resolves direction-specific results, and never replays the operation which first fails. |
| Fixed-frame batching | Yes | In overrun mode the ordered queue packs complete 54-bit frames up to an optional wire limit; simple mode remains sequential. Operations in a failed physical chunk are indeterminate; later chunks remain unsent, and requested operations are never replayed. |
| Multidrop or dormant state | No | The public connection models one entered SWD target. |
| Behavioral simulation | Yes | Protocol entry and line-reset effects, live overrun response grammar, DP/AP register transfers, packed fixed frames, transfer limits, and request-phase WAIT or FAULT injection. |
| Physical DPIDR read | HIL | Opt-in FTDI test and trivial example on Linux and macOS. |

The public `swd.Wire` boundary permits another wire implementation, but FTDI
is the only physical implementation currently provided.
The [Serial Wire Debug guide](protocols/swd.md) gives the bit-level protocol,
specification notes, and current physical observation.

## Debug Access Port and MEM-AP

| Capability | Implemented | Validation and boundary |
| --- | --- | --- |
| DPIDR decoding | Yes | Validates the constant bit and exposes raw identity fields. |
| SW-DP connection | Yes | `DebugPort.Connect` uses the SWD connection's established DPIDR and response grammar, then requests acknowledged power. It records newly requested power bits before writing them and attempts bounded cleanup after failed setup. Failed cleanup remains retryable through `Release`. |
| SW-DP release | Yes | Restores SELECT to bank zero, settles that write through RDBUFF, clears only power requests acquired by the debug port, then releases the SWD connection. If framing is unknown, cleanup first performs bounded SWD re-entry and verifies DPIDR against the connection being cleaned up. Failed release can be retried and blocks ordinary operations in the meantime. |
| DP register access | Yes | Logical ADIv5 registers distinguish operations which share a wire offset, including DPIDR from ABORT and SELECT from RESEND. Access selects the required bank while preserving known AP fields. CTRL/STAT writes must preserve connection-owned ORUNDETECT. Wrong directions, unknown registers, unavailable DP versions, and writes requiring unsupported turnaround fail before traffic. DAPABORT invalidates AP-derived state. Access is blocked while cleanup is pending. |
| AP identity | Yes | `NewAPSel` constructs an AP selector whose zero value is invalid. `ReadAPIDR` reads the common read-only IDR, and `DecodeAPIDR` exposes its ADIv5 fields. |
| Raw AP access | Yes | `APSel.Address` combines a selector with a complete eight-bit register address; both types have invalid zero values. Immediate and queued operations reject invalid or unaligned addresses before traffic. Reads use RDBUFF and writes use a completion barrier. Either operation invalidates existing MEM-AP handles if it completes or might have completed. A request canceled before it is sent does not invalidate them. Raw access has the effects defined by the selected AP class; a MEM-AP data-register write can write target memory. APIDR writes fail before traffic. |
| Ordered transactions | Yes | A single-use queue validates every DP/AP operation before traffic and settles any earlier immediate DP write before the queue runs. Queued reads expose data through `ReadResult.Value`; queued writes expose completion through `WriteResult.Err`. DP writes and AP operations settle through RDBUFF. Ordinary fixed frames use SWD batches; DPIDR, CTRL/STAT, and ABORT remain physical boundaries. The queue retains a confirmed prefix after failure and distinguishes later unsent work from operations in a failed physical chunk. FT232H HIL completed nine requests in two SWDIO calls. |
| WAIT handling | Yes | After the SWD connection completes any required STICKYORUN cleanup, `DebugPort` retries only the physical request which returned WAIT. The one-argument constructor uses the operation context; `WithMaxWaits` sets an optional per-request response-count limit, and `SetMaxWaits` changes it only while the port is idle. A limit of one returns the first clean WAIT as `swd.ErrWait`. If the context ends, `errors.Is` reports its error and the original WAIT is not retained as `swd.ErrWait`; independently joined cleanup failures remain visible. Reaching either boundary after an AP WAIT uses DAPABORT. A FAULT ends retrying without losing framing. Failed WAIT cleanup or a later retry error leaves framing unknown, invalidates AP-derived state, and blocks all traffic except cleanup. |
| FAULT recovery | Yes | A FAULT is never replayed. With a known response grammar and bank-zero selection, the error includes the captured CTRL/STAT value and DAP clears only the sticky conditions reported there, then verifies that they are clear. A definitely abandoned AP write does not invalidate MEM-AP state; an uncertain effect does. Failed cleanup preserves the FAULT and blocks ordinary traffic until release repairs the port. |
| AP enumeration | Yes | Scans all 256 ADIv5 APSEL values in bounded transactions. IDR zero means absent; a FAULT returns the confirmed discoveries with the error. The current Cortex-M bench reports AP0 as `0x24770011` and AP1 as `0x02880000`; sparse numbering is covered by simulation. The scan used 32 SWDIO calls for 1,022 fixed frames, all with OK acknowledgements. |
| MEM-AP acquisition | Yes | `OpenMemAP` performs AP traffic, rejects an absent or non-MEM AP, and snapshots the state which `Release` restores. |
| MEM-AP configuration | Yes | `OpenMemAP` reads CFG, models BE, LA, and LD, and includes TARHI in retryable restoration when large addresses are available. |
| Scalar target-memory access | Yes | `ReadScalar` and `WriteScalar` support aligned 8-, 16-, and 32-bit values and verify the implementation-defined CSW.Size before using the byte lane selected by CFG.BE. CFG.LA permits addresses above 32 bits; CFG.LD makes 64-bit access eligible for the same CSW check. Oversized write values fail before traffic, and writes finish with an AP completion barrier. If the first DRW access of a failed Size64 transfer might have started, ordinary traffic remains blocked until cleanup. `ReadWord` provides the 32-bit convenience operation. |
| MEM-AP restoration | Yes | Saves and restores CSW, TAR, and TARHI when present; failed restoration remains retryable. MEM-AP restoration remains available while debug-port cleanup is pending. If framing is unknown, `Release` re-enters SWD before restoration. It terminates a possibly incomplete Size64 transfer through CSW before touching TAR or TARHI. If DAPABORT interrupts cleanup, the next `Release` retries every saved value. The invalidated handle remains invalid. |
| Managed target-memory writes | Yes | `WriteScalar` and `WriteBlock` are effectful. The caller selects the address; the API checks alignment and range, not whether that address is safe to modify. `WriteRawAP` remains an unmanaged escape hatch. |
| Block reads | Yes | Accepts empty, unaligned, and mixed-width ranges. No auto-incrementing word run crosses a 1 KiB TAR boundary. If the MEM-AP does not accept single address increment, the reader writes TAR before each word. It uses the ordinary DAP WAIT policy. If selection, framing, or cleanup becomes uncertain, repair is required. A FAULT returns only the confirmed prefix. Cancellation and transport or protocol failures can also interrupt the read. Unread destination bytes remain untouched. |
| Block writes | Yes | Uses the block-read geometry, bounded buffered-write chunks, and the ordinary DAP WAIT policy. If the MEM-AP does not accept single address increment, `WriteBlock` writes TAR before each word. An accepted write is not replayed; if its RDBUFF completion request returns WAIT, only that request is retried. Only chunks whose RDBUFF completion requests were accepted contribute to the returned prefix. If the current chunk might have been applied, `WriteBlock` returns `ErrIndeterminate` without retrying it. |
| ADIv6 or JTAG-DP | No | The public implementation is the current minimal ADIv5 SW-DP path. |
| Behavioral simulation | Yes | DP identity/power, posted AP access, and byte-addressed MEM-AP reads and writes in either target byte order. AP fixtures take `dap.APSel` values and reject duplicate selectors, zero APIDRs, non-MEM-AP identities passed to `AddMEMAP`, and unaligned target-word addresses. |
| DAP-composed SWD entry | HIL | The FT232H/Cortex-M AP, transaction, and MEM-AP tests each counted one SWD connection performed by `DebugPort.Connect`; the reconnect test counted two. |
| AP and MEM-AP access | HIL | Opt-in FTDI integration tests against an explicitly selected AP. One transaction clocked nine fixed requests in two SWDIO calls and received nine OK acknowledgements. A 64-byte block read matched scalar byte reads from the same SRAM range and counted 571 OK acknowledgements, no WAIT, FAULT, or invalid acknowledgement, and 563 fixed frames. Separately gated tests preserved that range, exercised 8-, 16-, and 32-bit scalar writes plus aligned 64-byte and unaligned 31-byte block writes, checked neighboring bytes, then restored and verified the original contents. The scalar-write test counted 3,130 OK acknowledgements and 3,122 fixed frames; the block-write test counted 777 OK acknowledgements and 769 fixed frames. Neither returned WAIT, FAULT, or an invalid acknowledgement. The selected range did not cross a TAR boundary, and the target did not advertise CFG.LD. |

Connecting and using a MEM-AP changes volatile debug state; its write methods
also change target memory. Applications must release the MEM-AP before the
debug port so CSW, TAR, and TARHI when present are restored, bank selection
returns to zero, and acquired power is released.
Calls which share a debug port, MEM-AP, or SWD connection must be serialized;
the packages do not add locking.
The [Arm Debug Access Port guide](ports/dap.md) describes ADIv5 register
access, posted transactions, power handshakes, and the current bench result.

## Cortex-M target operations

| Capability | Implemented | Validation and boundary |
| --- | --- | --- |
| CPUID read and decode | Yes | Accepts any aligned-word reader and validates a plausible Arm Cortex-M identity. |
| Physical identity read | HIL | Opt-in FTDI/SWD/DAP/MEM-AP integration test. |
| Halt, resume, or step | No | No target run-control API exists. |
| Register access | No | CPUID decoding is not a general core-register interface. |
| Reset | No | No architectural or pin-reset operation exists. |
| Breakpoints or watchpoints | No | No target instrumentation API exists. |
| Firmware or runtime loading | No | No ELF loader, image-placement policy, or flash driver exists. |

The package identifies a processor; it is not yet a complete Cortex-M target
driver.

## Executable surfaces

Available examples:

- `examples/trivial/swd-dpidr` reads one raw DPIDR.
- `examples/simple/ap-id` reports DPIDR and one explicitly selected AP IDR.
- `examples/simple/cortexm-info` reports DPIDR, AP IDR, and Cortex-M CPUID.

Available `ost` commands:

```text
ost ftdi list
ost swd dpidr
ost dap dp id
ost dap ap id --ap N
ost target cortex-m id --ap N
```

These hardware operations are read-only with respect to target memory and do
not halt or reset the target. They still claim the adapter, clock SWD, and use
the volatile DAP and MEM-AP state described above.

## Not currently provided

There is no public J-Link or CMSIS-DAP driver, JTAG protocol layer, automatic
probe discovery policy, CoreSight or ROM-table discovery,
multi-core or SoC attachment, general target control, semihosting, trace,
debugger protocol server, firmware flashing, FPGA programming, or Windows
host implementation.

Treat an absent capability as an explicit boundary. Do not infer it from the
project description or recreate its lower-level protocol inside an
application. See [Composing Ostiole](composition.md) for selecting and
extending the current layers.
