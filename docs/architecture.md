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
| `usb` | Enumerate, open, inspect the active standard configuration, claim, transfer through, and close one host USB attachment. |
| `ftdi` | Own one explicitly selected FTDI MPSSE port and expose direction-safe SWD bits. |
| `swd` | Enter SWD, establish its response grammar, and encode, execute, and validate individual or packed DP/AP register transactions. |
| `swd/sim` | Model SWD protocol entry, register transfers, fixed-frame packing, and transfer limits without hardware. |
| `dap` | Manage SW-DP identity and power, ordered DP/AP transactions, posted AP access, and scalar or block MEM-AP access. |
| `dap/sim` | Model the DP, AP, and byte-addressed target-memory state consumed by `dap`. |
| `target/cortexm` | Read and decode the architectural Cortex-M CPUID value. |
| `examples/...` | Demonstrate public package compositions as executable programs. |
| `cmd/ost` | Provide a small command hierarchy over the same public packages. |

Raw USB requests stay in `usb` and adapter drivers. MPSSE framing stays in
`ftdi`. SWD request, acknowledgement, and parity rules stay in `swd`.
Debug-port banking, AP identification, raw AP addresses, power ownership,
and MEM-AP state stay in `dap`. Higher layers should call these packages rather
than reproduce their framing.

## Discovery and opening

`usb.New` constructs access to the host USB inventory. `List` returns a
snapshot matching either one exact vendor/product pair or every product from a
vendor, without opening a device. The caller selects exactly one
`usb.DeviceInfo` and passes it back to `Open`.

`Open` revalidates the bus address and USB identity before and after acquiring
the attachment, then retains that identity for the driver. A device that
disappeared or changed identity returns `usb.ErrStaleCandidate` instead of
silently opening a replacement.

`ftdi.SupportedDevices` provides the USB identities understood by the FTDI
driver. It does not select a device. `ftdi.Open` derives the product from the
opened USB device; `ftdi.Config` selects the MPSSE port and a maximum SWD
clock. The channel reports the attainable clock it configured.

This split keeps inventory policy in the application. Listing hardware does
not claim an interface or send adapter or target traffic.

## Ownership and cleanup

The live path has one logical owner. Resources are acquired from the bottom
up and released in reverse order.

| Value | Ownership rule |
| --- | --- |
| `*usb.Enumerator` | Holds inventory configuration, not an open attachment. |
| `*usb.Device` | Owns one open attachment. `ClaimInterface` returns the sole owner of one interface; that value selects alternates and releases the claim. A failed release can be retried, and `Device.Close` does not close the attachment while release remains pending. |
| `*ftdi.Channel` | Takes ownership of the USB device after `ftdi.Open` succeeds. `Close` resets bit mode, sets the latency timer to 16 ms, purges the receive and transmit paths, releases the interface, and closes the device. A failed interface release leaves the channel and device open for another `Close`. It does not preserve prior FTDI settings. |
| `*swd.Conn` | Owns one logical SWD transaction stream and the ORUNDETECT bit it adds. `Connect` establishes the target's response grammar and `Release` restores the inherited setting. It does not own a separate host resource. Calls must be serialized. |
| `*dap.DebugPort` | Requires exclusive use of its SWD connection and owns only the debug and system power requests it adds. It records newly requested power bits before writing them so bounded cleanup can attempt to clear them even when the write's result is ambiguous. `Release` settles its final SELECT write through RDBUFF, releases power, then releases the SWD connection. |
| `*dap.MemAP` | `OpenMemAP` validates the selected AP and saves its CSW, TAR, and optional TARHI. `Release` retries failed restoration; if DAPABORT interrupts cleanup, the next `Release` retries every saved value. Calls sharing the MEM-AP or its debug port must be serialized. |

An application that reaches the MEM-AP layer releases the MEM-AP before the
debug port, then closes the FTDI channel. Cleanup errors remain meaningful and
should be joined with the operation error rather than discarded.

`dap.DebugPort` caches register-selection and AP state. Direct transfers
on its `swd.Conn` can make that cached state stale, so do not share the
connection with another transaction owner while the debug port remains in
use. No layer adds a mutex; serialization belongs to the composition.

Constructors and open operations attempt to clean up resources acquired before
a failed return. `ftdi.Open` takes ownership of its input only on success. After
an error, the caller closes the device; that call is harmless when `Open`
already completed cleanup and retries it otherwise.

## Protocol and policy boundaries

The FTDI channel clocks direction-explicit bit streams. It does not interpret
SWD requests. `swd.Conn` owns request framing, turnaround, acknowledgements,
data parity, line reset, the JTAG-to-SWD selection sequence, and the
CTRL/STAT.ORUNDETECT setting which selects the response grammar.

`swd.Conn.Connect` reads and validates DPIDR before configuration, clears
supported sticky state, writes zero to SELECT, settles it through RDBUFF, then
reads CTRL/STAT. This establishes which response grammar applies before
ordinary register access. It keeps an inherited ORUNDETECT setting or tries to
enable it; if the bit does not read back as set, the connection remains in
simple mode. `Release` restores the value found by `Connect`. Register methods
separate DP and AP reads from writes and reject an unsupported physical address
before sending traffic. They send the requested transaction once. In overrun
mode a returned WAIT also causes an ABORT write which clears STICKYORUN;
retrying the request remains the caller's decision.

`swd.Batch` validates its complete queue before traffic. It uses the response
grammar already established by `Connect`; callers cannot select another one.
Simple responses run one request at a time. Overrun responses pack complete
fixed frames up to the limit reported by the wire. A failed physical call makes
every operation in that chunk indeterminate and leaves later chunks unsent.
`Batch` assigns WAIT to the operation which received it after the connection
clears STICKYORUN; the connection does not replay that operation or its
abandoned suffix. See
[Serial Wire Debug](protocols/swd.md) for the wire protocol and current bench
notes.

`dap.DebugPort.Connect` asks the SWD connection to enter and establish framing
before applying ADIv5 policy. Public DP, AP, transaction, and MEM-AP operations
remain blocked until that connection is active. The debug port validates
DPIDR, gives each logical DP register its architectural direction and bank,
preserves the AP fields while changing DPBANKSEL, and requests acknowledged
debug and system power. CTRL/STAT writes preserve connection-owned ORUNDETECT,
and a non-default DLCR turnaround remains unsupported. The debug port retries
the exact physical request which returned WAIT and completes posted AP
transactions through RDBUFF. Ordered transactions use `swd.Batch` for ordinary
fixed frames but keep sticky-exempt DPIDR, CTRL/STAT, and ABORT operations at a
physical boundary. A packed WAIT retries the WAITed request and the suffix the
target abandoned; it does not repeat the confirmed prefix.
`NewAPSel` constructs an AP selector whose zero value is invalid.
`APSel.Address` combines it with a
complete eight-bit register address; the resulting `APAddress` also has an
invalid zero value. `ReadAPIDR` reads and decodes the common read-only AP
identity. Raw AP access rejects invalid or unaligned addresses before traffic.
Register names and effects remain specific to the selected AP class. A write
to a MEM-AP data register can write target memory. A raw AP read or write which
completes, or whose completion is uncertain, invalidates existing `MemAP`
values. `dap.DebugPort` retries the same physical request after a clean WAIT
until its response-count limit is reached or the operation context ends. The
one-argument constructor uses only the context; `WithMaxWaits` sets a limit,
and `SetMaxWaits` changes it while the port is idle. If either boundary ends AP
waiting, the debug port issues DAPABORT and invalidates AP-derived state.
RDBUFF also settles DP writes, but a stall or FAULT at that barrier does not
trigger AP-only recovery. A FAULT is not retried: the debug port captures
bank-zero CTRL/STAT, clears the sticky conditions reported there, verifies the
clear through CTRL/STAT, and returns a typed error. AP-derived state is
invalidated when the failed sequence might have changed it, but not when a
complete AP-write FAULT or WDATAERR establishes that the write was abandoned.
A SELECT write remains provisional until later traffic establishes whether
its data took effect. WDATAERR
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
available. `dap.MemAP` reads CFG, then uses one access port for aligned 8-,
16-, and 32-bit target-memory reads and writes when CSW accepts the selected
size. It also permits 64-bit transfers when CFG.LD is set and CSW accepts
Size64, and addresses above 32 bits when CFG.LA is set. If a Size64 transfer
fails after its first DRW access might have started, ordinary debug-port
traffic remains blocked until the MEM-AP and debug port are released. MEM-AP
cleanup terminates an incomplete transfer through CSW before restoring TAR or
TARHI. Arbitrary-range reads and writes use sub-word edges and bounded word
runs. No auto-incrementing word run crosses a 1 KiB TAR boundary. If CSW does
not retain single address increment, block access writes TAR before each word.
Scalar and block memory access use the same WAIT rule. An accepted write is not
replayed; if its RDBUFF completion request returns WAIT, only that request is
retried. If selection, framing, or cleanup becomes uncertain, the existing
repair behavior applies. A FAULT returns the confirmed prefix instead of
retrying the failed request.

ADIv5 access-port enumeration scans all 256 APSEL values in bounded
transactions. IDR zero means absent. The scan does not assume contiguous AP
numbers and reads no class-specific register.
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
target, write target memory, or change persistent state. The `dap.MemAP` API
does expose scalar and block target-memory writes; applications choose the
affected addresses and own the consequences.

The layers are not entirely passive:

- Opening FTDI claims a USB interface and places the selected function in
  MPSSE mode. Closing resets bit mode, sets the latency timer to 16 ms, purges
  the receive and transmit paths, releases the interface, and closes the USB
  device; it does not restore the function's prior FTDI settings.
- Entering SWD clocks line-reset and protocol-selection sequences.
- Connecting a debug port clears sticky status, selects a register bank, and
  may request volatile debug and system power.
- Raw AP access has the effects defined by the selected AP class. A raw read
  can change class-specific state, and a raw write to a MEM-AP data register
  can write target memory. `DebugPort` does not restore either operation.
- Reading or writing through a MEM-AP temporarily changes CSW, TAR, and
  sometimes TARHI, then restores their prior values.

Callers should always complete the documented release sequence, including
when the primary operation fails.
