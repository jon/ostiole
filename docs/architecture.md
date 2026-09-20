# Architecture

Ostiole separates host access, adapter behavior, wire protocols, debug-port
policy, and target-specific operations. Each package owns one kind of state
and exposes the smallest useful mechanism to the layer above it.

The current hardware paths are:

```text
USB host access
  |
  +-- FTDI MPSSE or J-Link --> JTAG TAPs and explicit chains
  |                             |
  |                             v
  |                           Arm JTAG-DP --> DAP transactions and MEM-AP
  |
  +-- FTDI MPSSE, J-Link, or CMSIS-DAP v2
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
| `usb` | Enumerate, open, inspect the active standard configuration, claim, transfer through, and close one host USB attachment, including explicit asynchronous bulk transfers. |
| `probe` | Own one supplied implementation and lend its supported protocol surfaces; the implementation retains its transport and cleanup obligations. |
| `armdebug` | Own a probe, its connected SW-DP or baseline JTAG-DP, and explicitly acquired MEM-APs; restore APs before releasing DAP and its connection or chain, then close the probe. |
| `discover` | Enumerate registered transports, classify probe bindings, and select one candidate without owning an open probe. |
| `discover/probes` | Register all bundled probe providers for generic tools. |
| `ftdi` | Own one explicitly selected FTDI MPSSE port and expose direction-safe SWD bits or packed JTAG clocks. |
| `jlink` | Find one reviewed J-Link USB application interface, own its command session, configure SWD or JTAG, and adapt scan v3 to the selected wire protocol. |
| `cmsisdap` | Shortlist CMSIS-DAP product strings, validate one explicitly selected v2 bulk interface, own its command session, configure SWD, and adapt packet-bounded sequence commands to direction-explicit SWD bits. |
| `swd` | Enter SWD, establish its response grammar, and encode, execute, and validate individual or packed DP/AP register transactions. |
| `jtag` | Track TAP state, scan registers, discover reset entries, measure IR length, and validate explicit chains for selected-TAP access over a caller-supplied wire. |
| `swd/sim` | Model SWD protocol entry, register transfers, fixed-frame packing, and transfer limits without hardware. |
| `dap` | Bind SW-DP or baseline ADIv5 JTAG-DP, manage identity and power, execute ordered DP/AP transactions, and provide scalar or block MEM-AP access. |
| `dap/sim` | Model the DP, AP, and byte-addressed target-memory state consumed by `dap`. |
| `coresight` | Identify debug components and walk ROM tables through borrowed scalar memory, with explicit bounds and no resource acquisition or target-memory writes. |
| `target/cortexm` | Read and decode the architectural Cortex-M CPUID value. |
| `examples/...` | Demonstrate public package compositions as executable programs. |
| `cmd/ost` | Provide a small command hierarchy over the same public packages. |

Raw USB requests stay in `usb` and adapter drivers. MPSSE framing stays in
`ftdi`. SWD request, acknowledgement, and parity rules stay in `swd`.
Debug-port banking, AP identification, raw AP addresses, power ownership,
and MEM-AP state stay in `dap`. Higher layers should call these packages rather
than reproduce their framing.

## Discovery and opening

`discover.Registry` stores immutable transport providers. Registration is
serialized; enumeration runs outside the lock against a provider snapshot.
Core discovery imports no concrete transport. Applications register providers
explicitly or use the package registry. Each call enumerates each provider once.

Import `usb/discovery` to register all-device USB enumeration, or call its
`Register` with an explicit registry. Its detached `Attachment` retains the
exact `usb.DeviceInfo` snapshot for later opening. USB identity breaks final
ordering ties by numeric bus/address and VID/PID; missing or duplicate serials
cannot keep physical-device ordering stable when replugging changes addresses.

Each concrete driver exposes `OpenProbe` to acquire an exact USB attachment
under a generic `probe.Probe`. Interface claim and protocol configuration happen
when the owner lends SWD, or JTAG for FTDI and J-Link. The concrete adapters
share the USB-to-session ownership transition and retain either the
attachment or returned session after failed activation, so the generic owner
can retry cleanup. FTDI's ownership adapter retains its channel after failed
setup so a later close can retry the MPSSE reset sequence if the initial bulk
drain failed.

`usb.New` constructs access to the host USB inventory. `List` returns a
snapshot matching every attachment, one exact vendor/product pair, or every
product from a vendor, without opening a device. Each candidate includes the
host-visible USB product and serial strings when available. The caller selects
exactly one `usb.DeviceInfo` and passes that complete snapshot back to `Open`.

`Open` revalidates the bus address, vendor ID, product ID, product string, and
serial before and after acquiring the attachment, then retains that identity
for the driver. A device that disappeared or changed identity returns
`usb.ErrStaleCandidate` instead of silently opening a replacement.

`ftdi.SupportedDevices` provides the USB identities understood by the FTDI
driver. It does not select a device. `ftdi.Open` derives the product from the
opened USB device; `ftdi.Config` selects the MPSSE port and a maximum wire
clock. The channel reports the attainable clock it configured.

`ftdi.Open` initializes MPSSE and the clock with target pins as inputs.
The same `Channel` supplies `SWDIO` and `JTAGIO`; each nonempty call establishes
its pin directions before clocking. No protocol or wiring selector is needed.
A non-nil channel owns the attachment even on error; a nil result leaves it
with the caller. Calls are serialized. Do not mix raw traffic underneath a
borrowed SWD or JTAG connection, whose protocol state that traffic invalidates.
Release higher-level state before closing the channel. No reset pin is driven.

`jlink.SupportedDevices` likewise returns exact candidate identities rather
than a vendor wildcard. `jlink.Open` inspects the active descriptors, rejects
missing or ambiguous application interfaces, selects the descriptor-chosen
alternate, resolves its active bulk endpoints, and reads metadata. With no
options it does not select a target interface. An immediate reopen may briefly
find the probe unconfigured; `jlink.Open` retries only that typed USB state for
at most one second. `jlink.WithSWD` selects SWD during open and requests a
whole-kHz clock no greater than the requested ceiling.
`WithJTAG` does the same for the advertised JTAG interface. An open session
can be explicitly reconfigured after releasing its existing protocol owner;
`SWDIO` and `JTAGIO` reject calls for the wrong selected interface. J-Link
probe owners defer session opening and configuration until SWD or JTAG is
requested.

CMSIS-DAP has no equivalent numeric identity catalog. Applications explicitly
request `usb.AllDevices`, may use `cmsisdap.Candidates` as a case-sensitive
product-string shortlist, and still select one complete attachment before
opening it. The shortlist is not protocol evidence. `cmsisdap.Open` validates
the exact v2 bulk-interface class and descriptor order. An explicitly selected
composite attachment can be opened even when its device product string lacks
the marker. With no options, open sends `DAP_Info` but no `DAP_Connect` command.
`cmsisdap.WithSWD` connects only the advertised SWD port and requests a maximum
target clock; `ConfigureSWD` applies the same configuration to an open session.

This split keeps inventory policy in the application. Listing hardware does
not claim an interface or send adapter or target traffic.

## Ownership and cleanup

The live path has one logical owner. Resources are acquired from the bottom
up and released in reverse order.

| Value | Ownership rule |
| --- | --- |
| `*usb.Enumerator` | Holds inventory configuration, not an open attachment. |
| `*usb.Device` | Owns one open attachment. `ClaimInterface` returns the sole owner of one interface; that value reads the selected alternate before its first endpoint lookup, selects later alternates explicitly, submits bulk transfers, and releases the claim. A successful macOS alternate selection retains the active pipe properties IOKit reports. A failed alternate selection invalidates cached endpoint state so the next lookup reads the host state again. A failed release can be retried, and `Device.Close` does not close the attachment while release remains pending. |
| `*usb.BulkTransfer` | Represents one request on one active bulk endpoint. Its buffer length is the requested transfer length; the endpoint address supplies direction. `Wait` reports the exact count for a successful short or zero-length completion, and ending the wait context does not cancel the request. A host-engine failure can end `Wait` before `Done` closes; the buffer remains host-owned until `Done`. `AbortBulk` cancels and performs a bounded drain of every pending request on the named endpoint. Failed cancellation or drain retains the requests and claim for another cleanup attempt. Closing the claim applies the same bound before release. |
| `*ftdi.Channel` | Takes ownership of the USB device whenever `ftdi.Open` returns a non-nil channel, including on error. It keeps enough ordered maximum-packet-sized IN requests armed to cover its largest response and consumes FTDI status-only completions independently of MPSSE writes. An ambiguous transfer or asynchronous receive failure poisons the channel before later traffic can use the command stream. Recovery requires closing it and opening a new one. `Close` drains bulk OUT before resetting bit mode, setting the latency timer to 16 ms, purging the receive and transmit paths, releasing the interface, and closing the device. A failed cancellation or interface release leaves the channel and device open for another `Close`. It does not preserve prior FTDI settings. |
| `*jlink.Session` | Takes ownership of the USB device after `jlink.Open` succeeds. Metadata-only open claims the descriptor-selected application interface, resolves its active endpoint properties, and leaves target configuration unchanged. SWD or JTAG configuration selects that interface and sets volatile probe clock state; `Close` does not restore an unknown prior interface or clock. A complete nonzero scan status requires explicit reconfiguration. An ambiguous bulk exchange or abandoned response poisons the session, and later commands require closing it and explicitly reopening the device. Starting Close blocks configuration and scans; a failed interface release leaves cleanup retryable. Device close runs once, and later calls return its cached result. |
| `*cmsisdap.Session` | Takes ownership of the USB device after `cmsisdap.Open` succeeds. It claims one descriptor-selected v2 command interface and uses the probe's negotiated packet size, with one full response IN request submitted before each command OUT request. Metadata-only open sends no target-port command. `WithSWD` or `ConfigureSWD` connects the SWD port and requests a maximum clock. After failed SWD configuration, `Open` makes a bounded cleanup attempt; if a synchronized disconnect remains pending, it returns the session with the error. After a poisoned exchange, `Close` reports the abandoned port and continues USB cleanup without sending another command. Device close runs once, and later calls return its cached result. |
| `*swd.Conn` | Owns one logical SWD transaction stream and the ORUNDETECT bit it adds. `Connect` establishes the target's response grammar and `Release` restores the inherited setting. It does not own a separate host resource. Calls must be serialized. |
| `*dap.DebugPort` | Requires exclusive use of its SWD connection or JTAG chain. It owns only the power requests it adds; JTAG also owns its temporary change to inherited ORUNDETECT. It records newly requested power bits before writing them so bounded cleanup can attempt to clear them even when the write's result is ambiguous. `Release` settles outstanding requests, restores owned power/control state, then releases the connection or chain without closing the probe. |
| `*dap.MemAP` | `OpenMemAP` validates the selected AP and saves its CSW, TAR, and optional TARHI. `Release` retries failed restoration; if DAPABORT interrupts cleanup, the next `Release` retries every saved value. Calls sharing the MEM-AP or its debug port must be serialized. |

An application that reaches the MEM-AP layer releases the MEM-AP before the
debug port, then closes its FTDI channel, J-Link session, or CMSIS-DAP session.
A metadata-only CMSIS-DAP composition closes its session directly. Cleanup
errors remain meaningful and should be joined with the operation error rather
than discarded.

`dap.DebugPort` caches register-selection and AP state. Direct transfers
on its SWD connection or JTAG chain can make that cached state stale, so do not
share either with another transaction owner while the debug port remains in
use. No layer adds a mutex; serialization belongs to the composition.

Constructors and open operations attempt to clean up resources acquired before
a failed return. A non-nil result from `ftdi.Open` retains cleanup obligations
in the returned channel; close that channel and retain it if cleanup fails.
Only a nil result leaves device cleanup with the caller. A failed `cmsisdap.Open`
can return a non-nil session when synchronized target-port cleanup
remains retryable; the caller closes that session before the device.

## Protocol and policy boundaries

USB owns the v2 interface descriptors and asynchronous bulk-transfer
mechanism. `cmsisdap` owns the product-marker convention, exact interface and
endpoint fingerprint, command and response framing, `DAP_Info` decoding,
negotiated packet limits, target-port connection, maximum-clock request,
packet-bounded `DAP_SWD_Sequence` framing, and the rule that a response request
is submitted before its command. Packet count remains metadata; the driver
does not pipeline commands.

The FTDI channel and configured J-Link or CMSIS-DAP session clock
direction-explicit bit streams. None interprets SWD requests. FTDI owns MPSSE
framing; J-Link owns its scan framing and status; CMSIS-DAP owns its sequence
commands and response status. Each adapter owns its clock and transfer limits.
`swd.Conn` owns request framing, turnaround,
acknowledgements, data parity, line reset, the JTAG-to-SWD selection sequence,
and the CTRL/STAT.ORUNDETECT setting which selects the response grammar.

`swd.Conn.Connect` tries JTAG-to-SWD first. If the initial DPIDR read has an
invalid ACK and its trailing clocks complete, it tries JTAG-to-dormant and
dormant-to-SWD once before reading DPIDR again. Release uses the same fallback
when it must repair framing. Release leaves SWD selected rather than restoring
the prior interface mode.

The connection reads and validates DPIDR before configuration, clears
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

`dap.DebugPort.Connect` returns a `dap.Identity`, whose accessors distinguish
DPIDR from IDCODE without treating their encodings as interchangeable. The
SW-DP connection establishes only DPIDR; JTAG-DP establishes only IDCODE.
Construct the opaque binding with `dap.SWDP(conn)` or
`dap.JTAGDP(chain, tapIndex)` and pass it to `dap.NewDebugPort`. The zero
binding is invalid; constructors send no traffic. The debug port enters the
bound protocol before applying DAP policy.
Public DP, AP, transaction, and MEM-AP operations
remain blocked until that connection is active. The debug port validates
register availability and direction for the binding. On SWD it validates
DPIDR, gives each logical DP register its architectural bank,
preserves the AP fields while changing DPBANKSEL, and requests acknowledged
debug and system power. CTRL/STAT writes preserve connection-owned ORUNDETECT,
and a non-default DLCR turnaround remains unsupported. The debug port retries
the exact physical request which returned WAIT and completes posted AP
transactions through RDBUFF. Ordered transactions use `swd.Batch` for ordinary
fixed frames but keep sticky-exempt DPIDR, CTRL/STAT, and ABORT operations at a
physical boundary. A packed WAIT retries the WAITed request and the suffix the
target abandoned; it does not repeat the confirmed prefix.
The private SWD executor owns request retries, sticky-fault recovery, and the
SELECT confirmations that depend on SWD response grammar. It resolves wire
batch results before returning them to the transaction policy. Unsent and
rejected requests remain distinct from confirmed transfers and ambiguous
exchanges; read parity errors still report
that the request was accepted, without claiming that its data is valid.
The executor also completes posted AP operations and reports confirmed block
writes and uncertain effects without making MEM-AP interpret SWD errors.
After interpreting SWD responses, the executor adds DAP error classifications
to completed operations and individual transaction results while retaining
their original causes.

Connection setup validates the context and options before protocol entry.
Power acquisition starts only after entry establishes the identity and
control state; failed setup and ordinary release use the same link cleanup.

ADIv6 SW-DP selects AP register addresses through SELECT and SELECT1, using
the width advertised by DPIDR1. Its transactions complete each operation
before sending the next. ADIv6 MEM-APs use the existing restoration and
managed ownership rules.

The private JTAG executor owns DPACC/APACC framing and its delayed-response
pipeline. It polls an accepted request to completion without replaying it,
then checks CTRL/STAT after each AP operation before another AP operation is
issued. JTAG uses the same `Txn`, AP selection, and `OpenMemAP` APIs with
sequential logical execution. Baseline SELECT is readable, IDCODE is distinct
from DPIDR, and later banked registers are rejected. The binding temporarily
disables inherited ORUNDETECT and rejects active pushed-operation or
transaction-counter modes. Release restores acquired control state and parks
the chain in BYPASS/Idle. After losing scan state, `DebugPort` revalidates the
exact chain and reacquires its TAP before restoring state. `WithCleanupTimeout`
bounds each independent recovery attempt: one second by default for SWD,
thirty for JTAG.
No driver is reopened automatically after a poisoned exchange.

`NewAPSel` constructs an ADIv5 index; `APAt` constructs an ADIv6 base-address
selector. Both return `APSel` values; the zero `APSel` remains invalid.
`APSel.Address` combines a selector with an eight-bit ADIv5 or twelve-bit
ADIv6 register offset; the
resulting `APAddress` also has an invalid zero value. `ReadAPIDR` reads and
decodes the common read-only AP identity. Raw AP access rejects invalid or
unaligned addresses before traffic. Register names and effects remain
specific to the selected AP class. A write to a MEM-AP data register can
write target memory. A raw AP read or write which completes, or whose
completion is uncertain, invalidates existing `MemAP` values. On SWD,
`dap.DebugPort` retries the same physical request after a clean WAIT until
its response-count limit is reached or the operation context ends. The
one-argument constructor uses only the context; `WithMaxWaits` sets a limit,
and `SetMaxWaits` changes it while the port is idle. If either boundary ends
AP waiting, the debug port issues DAPABORT and invalidates AP-derived state.
RDBUFF also settles DP writes, but a stall or FAULT at that barrier does not
trigger AP-only recovery. A FAULT is not retried: the debug port captures
bank-zero CTRL/STAT, clears the sticky conditions reported there, verifies
the clear through CTRL/STAT, and returns a typed error. AP-derived state is
invalidated when the failed sequence might have changed it, but not when a
complete AP-write FAULT or WDATAERR establishes that the write was
abandoned. A SELECT write remains provisional until later traffic
establishes whether its data took effect. WDATAERR invalidates the cached
selection; FAULT handling reads `0x04` only when both possible DP banks are
zero. If FAULT cleanup, WAIT cleanup, or another transfer leaves framing
unknown, `dap.DebugPort` invalidates AP-derived state and blocks every
operation except cleanup. Cleanup re-enters SWD before sending another
framed request and refuses to restore state if DPIDR no longer matches the
connection being cleaned up. Failed setup uses the DPIDR read by that
attempt; cleanup for an established connection uses its last successful
DPIDR. `Connect` attempts this cleanup itself when setup fails; a cleanup
failure remains pending for `Release`. Once `Release` starts, a failure
likewise leaves only `MemAP.Release`, `DebugPort.Release`, and the cached
identity available. `dap.MemAP` reads CFG, then uses one access port for
aligned 8-, 16-, and 32-bit target-memory reads and writes when CSW accepts
the selected size. It also permits 64-bit transfers when CFG.LD is set and
CSW accepts Size64, and addresses above 32 bits when CFG.LA is set. If a
Size64 transfer fails after its first DRW access might have started,
ordinary debug-port traffic remains blocked until the MEM-AP and debug port
are released. MEM-AP cleanup terminates an incomplete transfer through CSW
before restoring TAR or TARHI. Arbitrary-range reads and writes use sub-word
edges and bounded word runs. No auto-incrementing word run crosses a 1 KiB
TAR boundary. If CSW does not retain single address increment, block access
writes TAR before each word. Scalar and block memory access use the same
WAIT rule. An accepted write is not replayed; if its RDBUFF completion
request returns WAIT, only that request is retried. If selection, framing,
or cleanup becomes uncertain, the existing repair behavior applies. A FAULT
returns the confirmed prefix instead of retrying the failed request.

ADIv5 access-port enumeration scans all 256 APSEL values in bounded
transactions. IDR zero means absent. The scan does not assume contiguous AP
numbers and reads no class-specific register.
See [Arm Debug Access Ports](ports/dap.md) for the ADIv5 register protocol and
the awkward parts of posted and memory access.

`dap.MemAP.ReadDebugBase` reads and decodes the selected AP's advertised
debug entry, including legacy encodings and the optional upper address word.
It preserves the memory client's state on success and does not access target
memory. `coresight` reads component identification through a scalar-memory
reader. It also derives ROM geometry from those identities, reads individual
entries, and walks hierarchies with explicit limits. Traversal skips
children with power-domain metadata and reports an incomplete result. It
uses DAP transfer sizes but owns no DAP or MEM-AP state. See [CoreSight
component identity](coresight.md) for its register and failure boundaries.

`target/cortexm` depends only on a compatible word reader. It knows the CPUID
address and encoding, but it does not know about USB, FTDI, or SWD.

## Host implementations

The `usb` API is the same on both supported hosts:

- Linux uses sysfs for inventory and usbfs for ownership and transfers. Each
  bulk request is one pinned usbfs URB. One claim-owned worker submits, reaps,
  cancels, and drains those URBs while a companion waits for completion
  readiness on the usbfs descriptor. A terminal readiness or reap failure
  stops new submissions and wakes pending waits without releasing buffers
  still owned by the kernel. It is implemented in pure Go.
- macOS uses cgo with the system IOKit and CoreFoundation frameworks. Claiming
  an interface temporarily seizes that interface from its current driver and
  closing it releases ownership. A claim-owned worker confines asynchronous
  pipe operations and the interface event source to one locked OS thread and
  run loop.

The API keeps USB endpoint addresses, directions, maximum packet sizes,
transfer lengths, completion boundaries, short transfers, and zero-length
transfers visible. It does not pair IN with OUT, flatten endpoints into byte
streams, choose buffer sizes, or rearm reads. Adapter drivers submit as many
requests as their protocols require and interpret each completion themselves.
When a driver treats several IN requests as one ordered flow, it consumes their
handles in submission order.

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
- Configuring J-Link SWD or JTAG selects the probe's target interface and
  changes its volatile target clock. Closing releases the application
  interface and USB device but does not restore an unknown prior interface
  or clock.
- Opening a CMSIS-DAP v2 session claims its command interface and exchanges
  metadata commands. With SWD configuration, it also initializes the probe's
  SWD pins and requests a volatile maximum target clock. Close attempts
  `DAP_Disconnect`. The package does not connect JTAG or use the optional SWO
  endpoint.
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
