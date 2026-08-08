# Composing Ostiole

Choose the highest-level package that already owns the behavior an
application needs. Drop to a lower layer only when the lower-level operation
is itself the goal.

For example, a program identifying a Cortex-M should call `cortexm.Identify`
rather than read and decode CPUID itself. A program inspecting an access port
should call `dap.DebugPort.ReadAP` rather than manually implement the posted
read pipeline with SWD transactions.

## Find the right layer

| Task | Public API | Executable reference |
| --- | --- | --- |
| List USB attachments understood by the FTDI driver | `usb.New`, `ftdi.SupportedDevices`, `Enumerator.List` | `ost ftdi list` |
| Open one FTDI MPSSE SWD port | `Enumerator.Open`, `ftdi.Open` | `examples/trivial/swd-dpidr` |
| Enter SWD mode or transfer one DP/AP register | `swd.New`, `Conn.JTAGToSWD`, `Conn.Transfer` | `examples/trivial/swd-dpidr` |
| Decode a DPIDR and manage SW-DP power | `dap.NewSWDP`, `DebugPort.Connect`, `DebugPort.Release` | `ost dap dp id` |
| Access one explicitly selected AP register | `DebugPort.ReadAP`, `DebugPort.WriteAP` | `examples/simple/ap-id` |
| Read one aligned target word through a MEM-AP | `dap.NewMemAP`, `MemAP.ReadWord`, `MemAP.Release` | `examples/simple/cortexm-info` |
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

Pass the selected product identifier into `ftdi.Config` along with the MPSSE
port, SWD interface, and requested clock. The driver validates this
combination; discovery does not infer it.

`ftdi.Open` takes ownership of the `*usb.Device` on both success and failure.
After a successful call, close the returned channel rather than separately
closing the device.

## Choose between raw SWD and DAP

Use `swd.Conn` when the application needs one explicit wire-protocol
transaction or is bringing up an SWD path. It returns WAIT, FAULT, parity, and
protocol errors directly and does not retry them.

Use `dap.DebugPort` when the application needs debug-port identity, power
ownership, bank selection, or AP access. Call `Connect` before AP operations
and `Release` afterward. This layer owns the posted AP read and write
completion rules.

Use `dap.MemAP` when the application needs the currently supported target
memory operation: one aligned 32-bit read through an explicitly selected
MEM-AP. Keep the debug port connected until `MemAP.Release` restores CSW and
TAR.

Use `target/cortexm` when the desired result is processor identity. It accepts
the word-reader behavior supplied by `dap.MemAP`, so target code remains
independent of the host, adapter, and wire protocol.

## Release in reverse order

A complete Cortex-M identity composition acquires and releases state in this
order:

```text
acquire: USB device → FTDI channel → SWD mode → debug port → MEM-AP
release: MEM-AP → debug port → FTDI channel
```

Use a fresh, bounded cleanup context if the operation context may already be
canceled. Join cleanup errors with the operation error so a restoration or
close failure is not lost.

The current open paths clean up host resources acquired before returning an
error. A successfully returned value belongs to the caller until its
documented release or close method succeeds.

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

1. Confirm the behavior appears in the task table or matching package
   documentation.
2. Select the highest package in the task table that owns the desired result.
3. Follow the matching executable example for acquisition and cleanup.
4. Import public packages only; do not import `cmd/ost/internal/...`.
5. Preserve explicit selection, deadlines, cleanup, and safety effects.
6. If the capability is missing, make that gap explicit instead of
   reimplementing lower-level framing in application code.

The [architecture guide](architecture.md) is the authority for current package
ownership. The [examples](../examples) are the authority for compact,
executable composition.
