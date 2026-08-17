# Architecture

Ostiole separates host access, adapter behavior, wire protocols, debug-port
policy, and target-specific operations. Each package owns one kind of state
and exposes the smallest useful mechanism to the layer above it.

The current hardware path is:

```text
USB host access
      |
      v
FTDI MPSSE adapter
      |
      v
Serial Wire Debug
      |
      v
Arm Debug Port and MEM-AP
      |
      v
Cortex-M identity
      |
      v
examples and ost
```

Programs can stop at any layer. Reading a raw SWD register does not require a
MEM-AP or target package, and identifying a Cortex-M does not require a
debugger service.

## Package responsibilities

| Package | Responsibility |
| --- | --- |
| `usb` | Enumerate, open, claim, transfer through, and close one host USB attachment. |
| `ftdi` | Own one explicitly selected FTDI MPSSE port and expose direction-safe SWD bits. |
| `swd` | Enter SWD mode and encode, execute, and validate individual DP/AP register transactions. |
| `swd/sim` | Model SWD protocol entry and basic register transfers without hardware. |
| `dap` | Manage SW-DP identity and power, ordered DP/AP transactions, posted AP access, and one MEM-AP view. |
| `dap/sim` | Model the DP, AP, and target-word state consumed by `dap`. |
| `target/cortexm` | Read and decode the architectural Cortex-M CPUID value. |
| `examples/...` | Demonstrate public package compositions as executable programs. |
| `cmd/ost` | Provide a small command hierarchy over the same public packages. |

Raw USB requests stay in `usb` and adapter drivers. MPSSE framing stays in
`ftdi`. SWD request, acknowledgement, and parity rules stay in `swd`.
Debug-port banking, posted AP reads, power ownership, and MEM-AP state stay in
`dap`. Higher layers should call these packages rather than reproduce their
framing.

## Discovery and opening

`usb.New` constructs access to the host USB inventory. `List` returns a
snapshot matching either one exact vendor/product pair or every product from a
vendor, without opening a device. The caller selects exactly one
`usb.DeviceInfo` and passes it back to `Open`.

`Open` revalidates the bus address and USB identity before and after acquiring
the attachment. A device that disappeared or changed identity returns
`usb.ErrStaleCandidate` instead of silently opening a replacement.

`ftdi.SupportedDevices` provides the USB identifiers understood by the FTDI
driver. It does not select a device. `ftdi.Open` still requires an explicit
product, MPSSE port, SWD interface, and clock in `ftdi.Config`.

This split keeps inventory policy in the application. Listing hardware does
not claim an interface or send adapter or target traffic.

## Ownership and cleanup

The live path has one logical owner. Resources are acquired from the bottom
up and released in reverse order.

| Value | Ownership rule |
| --- | --- |
| `*usb.Enumerator` | Holds inventory configuration, not an open attachment. |
| `*usb.Device` | Owns one open attachment and at most one claimed interface. `Close` releases both. |
| `*ftdi.Channel` | Takes ownership of the USB device passed to `ftdi.Open`. `Close` resets bit mode, sets the latency timer to 16 ms, purges the receive and transmit paths, releases the interface, and closes the device. It does not preserve prior FTDI settings. |
| `*swd.Conn` | Represents one logical SWD transaction stream over its wire. It does not own a separate host resource. Calls on a connection must be serialized. |
| `*dap.DebugPort` | Requires exclusive use of its SWD transaction stream. After `Connect`, it owns only the debug and system power requests it added. It records newly requested power bits before writing them so bounded cleanup can attempt to clear them even when the write's result is ambiguous. `Release` settles its final SELECT write through RDBUFF before reporting success. |
| `*dap.MemAP` | Saves the CSW and TAR values it changes. `Release` retries failed restoration; if DAPABORT interrupts cleanup, the next `Release` retries both saved values. Calls sharing the MEM-AP or its debug port must be serialized. |

An application that reaches the MEM-AP layer releases the MEM-AP before the
debug port, then closes the FTDI channel. Cleanup errors remain meaningful and
should be joined with the operation error rather than discarded.

`dap.DebugPort` caches response and register-selection state. Direct transfers
on its `swd.Conn` can make that cached state stale, so do not share the
connection with another transaction owner while the debug port remains in
use. No layer adds a mutex; serialization belongs to the composition.

Constructors and open operations clean up resources acquired before a failed
return. A caller owns only values returned successfully.

## Protocol and policy boundaries

The FTDI channel clocks direction-explicit bit streams. It does not interpret
SWD requests. `swd.Conn` owns request framing, turnaround, acknowledgements,
data parity, line reset, and the JTAG-to-SWD selection sequence.

The SWD transfer performs one physical transaction. A WAIT or FAULT
acknowledgement is returned to its caller without retrying. See
[Serial Wire Debug](protocols/swd.md) for the wire protocol and current bench
notes.

`dap.DebugPort` adds ADIv5 policy above SWD. It validates DPIDR, supports raw
current-bank and explicitly banked DP access, clears
supported sticky conditions with ABORT, writes zero to SELECT once without
retrying, confirms the write through RDBUFF, and rejects ORUNDETECT or a
non-default DLCR turnaround because the current SWD transfer does not implement
those response grammars. It then requests
acknowledged debug and system power, retries the exact physical request which
returned WAIT, and completes posted AP transactions through RDBUFF. After an
extended AP stall, `dap.DebugPort` issues DAPABORT and invalidates AP-derived
state. RDBUFF also settles DP writes, but a stall or FAULT at that barrier does
not trigger AP-only recovery. A FAULT is not retried: the debug port captures
bank-zero CTRL/STAT, clears the sticky conditions reported there, verifies the
clear through CTRL/STAT, returns a typed error, and invalidates AP-derived state
when the failed request belonged to AP work. A SELECT write remains provisional
until later traffic establishes whether its data took effect. WDATAERR
invalidates the cached selection; FAULT handling reads `0x04` only when both
possible DP banks are zero. If FAULT cleanup, WAIT cleanup, or another transfer
leaves framing unknown, `dap.DebugPort` invalidates AP-derived state and blocks
every operation except cleanup. Cleanup re-enters SWD before sending another
framed request and refuses to restore state if DPIDR no longer matches the
connection being cleaned up. Failed setup uses the DPIDR read by that
attempt; cleanup for an established connection uses its last successful
DPIDR. `Connect` attempts this cleanup itself when setup fails; a cleanup
failure remains pending for `Release`. Once `Release` starts, a failure likewise
leaves only `MemAP.Release`, `DebugPort.Release`, and the cached identity
available. `dap.MemAP` configures one access port for a single aligned 32-bit
read.
See [Arm Debug Access Ports](ports/dap.md) for the ADIv5 register protocol and
the awkward parts of posted and memory access.

`target/cortexm` depends only on a compatible word reader. It knows the CPUID
address and encoding, but it does not know about USB, FTDI, or SWD.

## Host implementations

The `usb` API is the same on both supported hosts:

- Linux uses sysfs for inventory and usbfs for ownership and transfers. It is
  implemented in pure Go.
- macOS uses cgo with the system IOKit and CoreFoundation frameworks. Claiming
  an interface temporarily seizes that interface from its current driver and
  closing it releases ownership.

The native implementation remains inside `usb`; packages above it do not
inspect host-specific state.

## Simulation boundary

The public simulations implement the same boundaries consumed by production
code. `swd/sim` provides a wire, and `dap/sim` provides DP, AP, and MEM-AP
state behind that wire.

Production packages do not import their simulators. Tests and downstream
programs may compose them explicitly, which keeps hardware-free behavior
replaceable while exercising the public protocol and DAP layers.

## Safety effects

The current examples and `ost` inspection commands do not reset or halt the
target, write target memory, or change persistent state.

The layers are not entirely passive:

- Opening FTDI claims a USB interface and places the selected function in
  MPSSE mode. Closing resets bit mode, sets the latency timer to 16 ms, purges
  the receive and transmit paths, releases the interface, and closes the USB
  device; it does not restore the function's prior FTDI settings.
- Entering SWD clocks line-reset and protocol-selection sequences.
- Connecting a debug port clears sticky status, selects a register bank, and
  may request volatile debug and system power.
- Reading through a MEM-AP temporarily changes CSW and TAR, then restores
  their prior values.

Callers should always complete the documented release sequence, including
when the primary operation fails.
