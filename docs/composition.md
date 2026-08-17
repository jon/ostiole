# Composing Ostiole

Choose the highest-level package that already owns the behavior an
application needs. Drop to a lower layer only when the lower-level operation
is itself the goal.

For example, a program identifying a Cortex-M should call `cortexm.Identify`
rather than read and decode CPUID itself. A program inspecting an access port
should call `dap.DebugPort.ReadAPIDR` rather than manually implement the posted
read pipeline with SWD transactions. Use `ReadRawAP` and `WriteRawAP` only when
the caller understands the selected AP class and will restore any state the
access changes. Derive the complete address with `APSel.Address`. A raw MEM-AP
data-register write can write target memory.

## Find the right layer

| Task | Public API | Executable reference |
| --- | --- | --- |
| List USB attachments understood by the FTDI driver | `usb.New`, `ftdi.SupportedDevices`, `Enumerator.List` | `ost ftdi list` |
| Open one FTDI MPSSE SWD port | `Enumerator.Open`, `ftdi.Open` | `examples/trivial/swd-dpidr` |
| Connect SWD or transfer DP/AP registers | `swd.New`, `Conn.Connect`, `Conn.ReadDP`, `Conn.WriteDP`, `Conn.ReadAP`, `Conn.WriteAP`, `Conn.NewBatch`, `Conn.Release` | `examples/trivial/swd-dpidr` |
| Enter SWD, decode a DPIDR, and manage SW-DP power | `dap.NewDebugPort`, `DebugPort.Connect`, `DebugPort.Release` | `ost dap dp id` |
| Identify one explicitly selected AP | `DebugPort.ReadAPIDR`, `DecodeAPIDR` | `examples/simple/ap-id` |
| Access another AP register by its full ADIv5 address | `DebugPort.ReadRawAP`, `DebugPort.WriteRawAP` | Package tests |
| Read or write one aligned target scalar through a MEM-AP | `dap.OpenMemAP`, `MemAP.ReadScalar`, `MemAP.WriteScalar`, `MemAP.Release` | `examples/simple/cortexm-info` uses `ReadWord`. |
| Read or write arbitrary target bytes through a MEM-AP | `dap.OpenMemAP`, `MemAP.ReadBlock`, `MemAP.WriteBlock`, `MemAP.Release` | Package tests |
| Identify a Cortex-M through any compatible word reader | `cortexm.Identify` | `examples/simple/cortexm-info` |
| Test SWD and DAP behavior without hardware | `swd/sim`, `dap/sim` | Package tests |

The examples are intentionally small, executable compositions of public
packages. `ost` adds command parsing and output policy, but its internal
packages are not a reusable library surface.

## Select and open hardware explicitly

Start with `usb.New` and list only the identities accepted by the intended
driver. Treat the result as a snapshot rather than a live handle.

The current examples require exactly one supported FTDI attachment. A larger
application can present the returned identities to a user, but it should
still make one explicit selection before calling `Open`. Do not silently pick
the first result from an ambiguous inventory.

Pass the opened device to `ftdi.Open` with the MPSSE port and maximum requested
clock. The driver reads and validates the product from the device identity;
discovery does not choose the port or clock.

`ftdi.Open` takes ownership of the `*usb.Device` when it succeeds. Close the
returned channel rather than separately closing the device. After an error,
call `Device.Close`. `Open` has already attempted cleanup, so that call either
finishes cleanup or harmlessly repeats it.

## Choose between raw SWD and DAP

Use `swd.Conn` when the application needs one explicit wire-protocol
transaction or is bringing up an SWD path. Call `Connect` before register
access and `Release` before closing the wire. `Connect` returns DPIDR, keeps
inherited ORUNDETECT or tries to enable it, and records whether the setting was
inherited. `Release` restores only a change made by that connection and can be
retried. A register operation returns WAIT, FAULT, parity, and protocol errors
without replaying the requested transaction. When a fixed response returns
WAIT, the connection clears STICKYORUN before returning it.
Use `Conn.NewBatch` for an ordered group of raw register operations. Queue each
operation with the direction-specific DP or AP method, call `Commit`, then read
each direction-specific result. The batch uses the connection's established
response grammar. In simple mode it sends one request at a time; in overrun
mode it packs complete fixed frames when the wire reports room for more than
one. A transport failure makes the operations in that physical chunk
indeterminate and leaves later chunks unsent. WAIT and FAULT still stop the
batch, and the connection never replays a requested operation.
The [SWD protocol guide](protocols/swd.md) describes the wire transaction and
the specification details which are easiest to misread.

Use `dap.DebugPort` when the application needs debug-port identity, power
ownership, bank selection, or AP access. Call `Connect` before AP operations
and `Release` afterward. `DebugPort.Connect` also connects its underlying SWD
stream, and `DebugPort.Release` releases it after restoring DAP state; do not
connect or release that stream separately. Give the debug port exclusive,
serialized use of its `swd.Conn`; direct transfers on that connection can
invalidate cached DAP state. DP, AP, transaction, and MEM-AP operations require
an active connection.
`ReadDP` and `WriteDP` take logical ADIv5 register names and manage DPBANKSEL
without exposing a current-bank API. `NewAPSel` constructs an AP selector whose
zero value is invalid. `APSel.Address` combines it with a complete eight-bit
register address; the resulting `APAddress` also has an invalid zero value.
`ReadAPIDR` reads and decodes the common read-only AP identity. `EnumerateAPs`
scans every ADIv5 AP selector without reading class-specific registers. Raw AP
access rejects an invalid or unaligned address before traffic. Use it only when
the caller understands the selected AP class and will restore any state the
access changes. A raw MEM-AP data-register write can write target memory. This
layer owns posted AP read and write completion and retries only the physical
request that returned WAIT. A raw AP read or write which completes, or might
have completed, invalidates existing `MemAP` values. After an extended AP stall,
`dap.DebugPort` issues DAPABORT; existing
`dap.MemAP` values reject further reads, though `dap.MemAP.Release` still
attempts to restore their saved state. The SWD connection reads DPIDR, clears
supported sticky conditions with ABORT, establishes bank zero through RDBUFF,
and establishes its response grammar before DAP requests power. Debug-port
CTRL/STAT writes must preserve ORUNDETECT. DAP settles a new SELECT through
RDBUFF before sending AP traffic. If WAIT cleanup or a later retry leaves
framing unknown, `dap.DebugPort` invalidates those values and later DP and AP
calls stop before sending traffic.
`Connect` performs bounded cleanup after failed setup. When cleanup succeeds,
the debug port can connect again immediately. If cleanup also fails, or if
`Release` fails, ordinary DP, AP, and MEM-AP operations remain blocked. Call
`MemAP.Release` before retrying `DebugPort.Release`; cleanup re-enters SWD when
necessary and verifies that DPIDR still identifies the connection being
cleaned up before restoring state. `DebugPort.Release` settles its final
bank-zero SELECT through RDBUFF, releases power, and restores connection-owned
ORUNDETECT before returning success.
The [DAP guide](ports/dap.md) describes the ADIv5 register protocol behind
that lifecycle.

Use `DebugPort.NewTxn` when several DP or AP accesses need ordered results.
`Commit` validates the complete queue, settles an earlier immediate DP write if
necessary, then lets the SWD connection pack fixed frames within its wire
limit. Sticky-exempt DPIDR, CTRL/STAT, and ABORT operations remain separate so
an earlier WAIT or FAULT cannot hide behind one of them. DP writes and AP
operations settle through RDBUFF before reporting success. Queued reads return a
`ReadResult`, whose `Value` method returns the data. Queued writes return a
`WriteResult`, whose `Err` method reports completion without a placeholder
value. If the earlier write cannot be settled, none of the queued operations
runs. Otherwise, confirmed results remain available after a failure; a clocked
operation whose completion is uncertain reports `ErrIndeterminate`, and later
operations report `ErrNotExecuted`. A transaction acquires no additional state
and does not change the release order.

Use `dap.MemAP` for aligned 8-, 16-, or 32-bit target-memory reads and writes
through an explicitly selected MEM-AP. Support for the non-word sizes is
implementation-defined, so each access verifies that CSW accepted its size
before touching memory. CFG.LD makes 64-bit access possible; CFG.LA permits
addresses above 32 bits. `target/cortexm` uses `ReadWord` for its 32-bit reads.

`MemAP.ReadBlock` accepts empty, unaligned, and mixed-width ranges. It retries
the same request after WAIT while selection and framing remain known, WAIT
cleanup succeeds, and its context remains active. If selection, framing, or
cleanup becomes uncertain, repair is required. A FAULT returns the contiguous
prefix read before the fault; cancellation and transport or protocol failures
can also interrupt the read. The rest of the destination remains unchanged. No
auto-incrementing word run crosses a 1 KiB TAR boundary; unaligned edges still
require the MEM-AP to accept byte or halfword CSW sizes. If the MEM-AP does not
accept single address increment, `ReadBlock` and `WriteBlock` write TAR before
each word.

`MemAP.WriteBlock` accepts the same ranges and uses the same geometry. Its
returned prefix includes only chunks whose RDBUFF completion requests were
accepted. If a failed chunk might already have changed memory, the error
includes `dap.ErrIndeterminate`; the method does not retry that chunk
automatically. An indeterminate chunk invalidates the `MemAP`, but `Release`
remains available to restore its saved state. `WriteBlock` retries a clean WAIT
on the same request until its context ends. An accepted write is not replayed;
if its RDBUFF completion request returns WAIT, only that request is retried.

`MemAP.WriteScalar` and `MemAP.WriteBlock` change target memory at the selected
addresses. The library rejects a scalar value which does not fit the selected
size and validates alignment, range, and advertised extensions, but deciding
whether an address is safe to write belongs to the application. Keep the debug
port connected until `MemAP.Release` restores CSW, TAR, and TARHI when present.
Calls sharing a MEM-AP, its debug port, or their SWD connection must be
serialized; these values do not add locking. If a Size64 transfer fails after
its first DRW access might have started, ordinary debug-port traffic remains
blocked. Release the MEM-AP so it can terminate the incomplete transfer through
CSW, then release and reconnect the debug port.

Use `target/cortexm` when the desired result is processor identity. It accepts
the word-reader behavior supplied by `dap.MemAP`, so target code remains
independent of the host, adapter, and wire protocol.

## Release in reverse order

A complete Cortex-M identity composition acquires and releases state in this
order:

```text
acquire: USB device → FTDI channel → debug port (enters SWD) → MEM-AP
release: MEM-AP → debug port → FTDI channel
```

Use a fresh, bounded cleanup context if the operation context may already be
canceled. Join cleanup errors with the operation error so a restoration or
close failure is not lost.

`Enumerator.Open` cleans up native resources before returning an error.
`ftdi.Open` attempts cleanup, then leaves the original device with the caller
for the `Device.Close` described above. A successfully returned value belongs
to the caller until its documented release or close method succeeds.

## Keep policy at the application edge

Applications own choices that depend on their users:

- which enumerated attachment to open;
- which explicit FTDI port and clock to request;
- which access port to inspect;
- operation and cleanup deadlines;
- how errors and identities are presented.

Libraries own reusable hardware and protocol behavior. USB requests, MPSSE
commands, SWD frames, AP posted reads, MEM-AP register restoration, and CPUID
decoding should not be recreated in an application.

If a needed capability is absent, add it at the layer that can express and
test it as a reusable mechanism. Do not hide a second hardware stack in an
example, an `ost` subcommand, or another application's command package.

## Guidance for coding agents

Before writing a hardware composition:

1. Read the [capability boundaries](capabilities.md) and confirm the behavior
   exists.
2. Select the highest package in the task table that owns the desired result.
3. Follow the matching executable example for acquisition and cleanup.
4. Import public packages only; do not import `cmd/ost/internal/...`.
5. Preserve explicit selection, deadlines, cleanup, and safety effects.
6. If the capability is missing, make that gap explicit instead of
   reimplementing lower-level framing in application code.

The [architecture guide](architecture.md) is the authority for current package
ownership. The [examples](../examples) are the authority for compact,
executable composition.
