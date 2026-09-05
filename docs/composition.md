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
| List every host USB attachment | `usb.New`, `usb.AllDevices`, `Enumerator.List` | Package tests |
| List USB attachments understood by the FTDI driver | `usb.New`, `ftdi.SupportedDevices`, `Enumerator.List` | `ost ftdi list` |
| Read metadata from one J-Link | `usb.New`, `jlink.SupportedDevices`, `Enumerator.Open`, `jlink.Open`, `Session.Info` | Package tests |
| Read metadata from one CMSIS-DAP v2 probe | `usb.New`, `usb.AllDevices`, `cmsisdap.Candidates`, `Enumerator.Open`, `cmsisdap.Open`, `Session.Info` | Package tests |
| Open one FTDI MPSSE SWD port | `Enumerator.Open`, `ftdi.Open` | `examples/trivial/swd-dpidr` |
| Open one J-Link SWD session | `Enumerator.Open`, `jlink.Open`, `jlink.WithSWD` | Package tests |
| Open one CMSIS-DAP SWD session | `Enumerator.Open`, `cmsisdap.Open`, `cmsisdap.WithSWD` | Package tests |
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

Transport discovery uses registered providers:

```go
transports, err := discover.Transports(ctx)
if err != nil {
    return err
}
for transport := range transports {
    fmt.Println(transport.Info())
}
```

Providers can instead register on a caller-owned `discover.Registry` with
`RegisterTransport`. `EnsureTransport` shares an identical provider dependency
without accepting a different provider under the same ID. Iteration repeats
the detached snapshot without enumerating again. To use `slices.Collect`,
convert the named sequence to `iter.Seq[discover.Transport]` explicitly.

An application with a `probe.SWDBackend` can transfer it to a generic owner:

```go
opened := probe.New(info, backend)
defer func() { err = errors.Join(err, opened.Close()) }()
wire, err := opened.SWD(ctx, probe.SWDConfig{MaxClockHz: 100_000})
if err != nil {
    return err
}
connection := swd.New(wire)
```

Use a named error result for this cleanup pattern. Release any SWD or DAP
state before closing the probe. The caller must stop using the transferred
backend directly; the borrowed wire has no independent close operation.

Concrete drivers can open that owner from an exact USB identity:

```go
opened, err := jlink.OpenProbe(ctx, identity)
// Or: ftdi.OpenProbe(ctx, identity, ftdi.PortA)
// Or: cmsisdap.OpenProbe(ctx, identity)
if err != nil {
    return err
}
defer func() { err = errors.Join(err, opened.Close()) }()
```

Start with `usb.New` and list only the identities accepted by the intended
driver. Treat the result as a snapshot rather than a live handle.

When a device family has no numeric identity catalog, `usb.AllDevices` makes
that broader inventory request explicit. The caller can inspect
`DeviceInfo.Product`, but a product string alone does not establish driver or
protocol support.

The current examples require exactly one supported FTDI attachment. A larger
application can present the returned identities to a user or match a known
nonempty `DeviceInfo.Serial`, but it should still make one explicit selection
before calling `Open`. The selected candidate retains its enumerated bus and
address; pass the complete value to `Open` so it can reject a replugged or
replaced attachment. Do not silently pick the first result from an ambiguous
inventory.

A CMSIS-DAP v2 metadata session begins with the explicit broad inventory:

```go
func openCMSISDAP(ctx context.Context) (_ *cmsisdap.Session, cleanup func() error, err error) {
    enumerator := usb.New()
    devices, err := enumerator.List(ctx, []usb.DeviceFilter{usb.AllDevices()})
    if err != nil {
        return nil, nil, err
    }
    candidates := cmsisdap.Candidates(devices)
    if len(candidates) != 1 {
        return nil, nil, fmt.Errorf("found %d CMSIS-DAP candidates, want one", len(candidates))
    }
    device, err := enumerator.Open(ctx, candidates[0])
    if err != nil {
        return nil, nil, err
    }
    session, err := cmsisdap.Open(ctx, device)
    if err != nil {
        closeErr := device.Close()
        if closeErr != nil {
            return nil, device.Close, errors.Join(err, closeErr)
        }
        return nil, nil, err
    }
    return session, session.Close, nil
}
```

On success the session owns the device, and `cleanup` calls `session.Close`.
After a failed open whose device cleanup also fails, `cleanup` calls
`device.Close` again. Retain a non-nil cleanup function. Calling it again
either retries a retained interface claim or returns the cached device-close
result.
Product matching is case-sensitive and only shortlists candidates; `Open`
still requires the exact v2 bulk interface. An application which knows a
composite probe by serial or another explicit policy may select it from
`devices` even when its device product string is absent from `Candidates`.

To use that selected v2 probe as an SWD wire, configure it during `Open` and
return a cleanup function even when `connection.Connect` fails:

```go
func connectCMSISDAPSWD(ctx context.Context, device *usb.Device) (_ uint32, cleanup func() error, err error) {
    session, err := cmsisdap.Open(ctx, device, cmsisdap.WithSWD(100_000))
    if err != nil {
        if session != nil {
            return 0, session.Close, err
        }
        closeErr := device.Close()
        if closeErr != nil {
            return 0, device.Close, errors.Join(err, closeErr)
        }
        return 0, nil, err
    }
    connection := swd.New(session)
    connectionOwned := true
    var abandoned error
    cleanup = func() error {
        cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
        defer cancel()
        if connectionOwned {
            if err := connection.Release(cleanupCtx); err != nil {
                if !errors.Is(err, cmsisdap.ErrSessionPoisoned) {
                    return err
                }
                abandoned = errors.Join(abandoned, fmt.Errorf("release SWD connection: %w", err))
            }
            connectionOwned = false
        }
        return errors.Join(abandoned, session.Close())
    }
    raw, err := connection.Connect(ctx)
    return raw, cleanup, err
}
```

`WithSWD` requires the advertised SWD capability, sends `DAP_Connect(SWD)`,
and requests a maximum frequency in hertz through `DAP_SWJ_Clock`.
`MaxClockHz` reports that accepted request because CMSIS-DAP does not report
the attained clock. `SWDIO` converts direction runs to packet-bounded
`DAP_SWD_Sequence` commands; the session remains the USB owner. A
`connection.Release` failure that leaves the session usable, a complete
`DAP_Disconnect` failure, or an interface-release failure leaves cleanup
retryable in reverse order. After a poisoned exchange, the helper records
`ErrSessionPoisoned`, lets `Close` report the abandoned port and finish USB
cleanup without another command, and returns that terminal error. Device close
runs once; later calls to `Session.Close` return its cached result.

A metadata-only J-Link session follows the same explicit inventory rule:

```go
func readJLinkInfo(ctx context.Context) (_ jlink.Info, cleanup func() error, err error) {
    enumerator := usb.New()
    candidates, err := enumerator.List(ctx, jlink.SupportedDevices())
    if err != nil {
        return jlink.Info{}, nil, err
    }
    if len(candidates) != 1 {
        return jlink.Info{}, nil, fmt.Errorf("found %d supported J-Links, want one", len(candidates))
    }
    device, err := enumerator.Open(ctx, candidates[0])
    if err != nil {
        return jlink.Info{}, nil, err
    }
    session, err := jlink.Open(ctx, device)
    if err != nil {
        closeErr := device.Close()
        if closeErr != nil {
            return jlink.Info{}, device.Close, errors.Join(err, closeErr)
        }
        return jlink.Info{}, nil, err
    }
    info := session.Info()
    if err := session.Close(); err != nil {
        return jlink.Info{}, session.Close, err
    }
    return info, nil, nil
}
```

Inventory policy still belongs to the application. `jlink.Open` takes
ownership on success. It claims only the J-Link application interface,
resolves the active endpoints after selecting its alternate, and does not
select or configure a target interface. If a close fails, the returned
`cleanup` function keeps the affected device or session reachable. Calling it
again either retries a retained interface claim or returns the cached
device-close result.

To use the same selected probe as an SWD wire, configure it while opening and
pass the session to `swd.New`. Keep the cleanup closure when the operation
fails: it retains the highest owner until release succeeds, and reconfigures
after a complete scan error before retrying SWD cleanup.

```go
func connectJLinkSWD(ctx context.Context, device *usb.Device) (_ uint32, cleanup func() error, err error) {
    session, err := jlink.Open(ctx, device, jlink.WithSWD(100_000))
    if err != nil {
        closeErr := device.Close()
        if closeErr != nil {
            return 0, device.Close, errors.Join(err, closeErr)
        }
        return 0, nil, err
    }
    connection := swd.New(session)
    connectionOwned := true
    var abandoned error
    cleanup = func() error {
        cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
        defer cancel()
        if connectionOwned {
            if session.ClockHz() == 0 {
                if err := session.ConfigureSWD(cleanupCtx, 100_000); err != nil {
                    if !errors.Is(err, jlink.ErrSessionPoisoned) {
                        return err
                    }
                    abandoned = errors.Join(abandoned, fmt.Errorf("reconfigure SWD for release: %w", err))
                    connectionOwned = false
                }
            }
            if connectionOwned {
                if err := connection.Release(cleanupCtx); err != nil {
                    if !errors.Is(err, jlink.ErrSessionPoisoned) {
                        return err
                    }
                    abandoned = errors.Join(abandoned, fmt.Errorf("release SWD connection: %w", err))
                }
                connectionOwned = false
            }
        }
        return errors.Join(abandoned, session.Close())
    }
    raw, err := connection.Connect(ctx)
    return raw, cleanup, err
}
```

`WithSWD` selects the advertised SWD interface and requests a whole-kHz clock
no greater than its argument. A metadata-only session can instead call
`ConfigureSWD` later. Both forms change volatile probe interface and clock
state, which `Close` does not restore. A complete nonzero scan status requires
another `ConfigureSWD`; an ambiguous transfer requires close and reopen. The
returned cleanup closure stops at a transient higher-level release failure and
can be retried. After a complete nonzero scan status, `ClockHz()` returns zero,
so the closure reconfigures SWD before retrying `swd.Conn.Release`; it does not
close the session while the connection still owns restorable target state.
`jlink.ErrSessionPoisoned` is different because restoration is no longer
possible. The closure records that restoration error, drops the higher-level
owner, continues with retryable session cleanup, and keeps returning the error
after the session closes.

Pass the opened device to `ftdi.Open` with the MPSSE port and maximum requested
clock. The driver reads and validates the product from the device identity;
discovery does not choose the port or clock.

`ftdi.Open` takes ownership of the `*usb.Device` when it succeeds. Close the
returned channel rather than separately closing the device. After an error,
call `Device.Close`. `Open` has already attempted cleanup, so that call either
finishes cleanup or harmlessly repeats it.

Adapter drivers submit USB transfers through the claimed interface and keep
their scheduling policy themselves:

```go
claim, err := device.ClaimInterface(0)
if err != nil {
    return err
}
endpoint, err := claim.Endpoint(ctx, 0x81)
if err != nil {
    return errors.Join(err, claim.Close())
}
if endpoint.TransferType != usb.TransferBulk || endpoint.MaxPacketSize == 0 {
    return errors.Join(errors.New("invalid bulk IN endpoint"), claim.Close())
}
buffer := make([]byte, endpoint.MaxPacketSize)
input, err := claim.SubmitBulk(ctx, endpoint.Address, buffer)
if err != nil {
    return errors.Join(err, claim.Close())
}
output, err := claim.SubmitBulk(ctx, 0x02, command)
if err != nil {
    return errors.Join(err, claim.AbortBulk(endpoint.Address), claim.Close())
}
if _, err := output.Wait(ctx); err != nil {
    return errors.Join(err, claim.AbortBulk(0x02), claim.AbortBulk(endpoint.Address), claim.Close())
}
received, err := input.Wait(ctx)
if err != nil {
    return errors.Join(err, claim.Close())
}
completion := buffer[:received]
if err := consume(completion); err != nil {
    return errors.Join(err, claim.Close())
}
return claim.Close()
```

This example posts one IN request before its OUT request. A protocol which
needs a receive window submits several maximum-packet-sized buffers instead;
one which does not tolerate read-ahead submits its IN request only when the
response is due. Each handle reports its own completion, including a short or
zero-length transfer. Ending a `Wait` context does not cancel the request.
If the host transfer engine fails, `Wait` returns that error while `Done` can
remain open; the caller must not reuse the buffer until `Done` closes.
`AbortBulk` is endpoint-wide and performs a bounded drain of every pending
request on that endpoint. A drain timeout matches `context.DeadlineExceeded`.
If native cancellation or the drain fails, those requests and the claim remain
owned so cleanup can be retried. Closing the claim applies the same bound
across its endpoints before release. The first endpoint lookup reads the
interface's current alternate setting; a new claim does not imply alternate
zero. If alternate selection fails, the next endpoint lookup reads the host
state again instead of retaining descriptors for the previous alternate.

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
request that returned WAIT. `dap.NewDebugPort(conn)` uses the operation context
as the retry bound. `dap.NewDebugPort(conn, dap.WithMaxWaits(1))` returns the
first clean WAIT as `swd.ErrWait`. `SetMaxWaits` changes the limit before
`Connect` or after a successful `Release`; it rejects the change while the port
is connected or cleanup is pending. The count is per physical request and does
not bound host I/O. A raw AP read or write which completes, or might have
completed, invalidates existing `MemAP` values. If the limit or context ends
after an AP WAIT, `dap.DebugPort` issues DAPABORT; existing `dap.MemAP` values
reject further reads, though `dap.MemAP.Release` still attempts to restore their
saved state.
The SWD connection reads DPIDR, clears supported sticky conditions with ABORT,
establishes bank zero through RDBUFF, and establishes its response grammar
before DAP requests power.
Debug-port CTRL/STAT writes must preserve ORUNDETECT. DAP settles a new SELECT
through RDBUFF before sending AP traffic. If WAIT cleanup or a later retry
leaves framing unknown, `dap.DebugPort` invalidates those values and later DP
and AP calls stop before sending traffic.
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

`MemAP.ReadBlock` accepts empty, unaligned, and mixed-width ranges. It uses the
same configured WAIT policy as the scalar and raw DAP operations. If selection,
framing, or cleanup becomes uncertain, repair is required. A FAULT returns the
contiguous prefix read before the fault; a configured WAIT limit, cancellation,
and transport or protocol failures can also interrupt the read. The rest of the
destination remains unchanged. No auto-incrementing word run crosses a 1 KiB
TAR boundary; unaligned edges still require the MEM-AP to accept byte or
halfword CSW sizes.
If the MEM-AP does not accept single address increment, `ReadBlock` and
`WriteBlock` write TAR before each word.

`MemAP.WriteBlock` accepts the same ranges and uses the same geometry. Its
returned prefix includes only chunks whose RDBUFF completion requests were
accepted. If a failed chunk might already have changed memory, the error
includes `dap.ErrIndeterminate`; the method does not retry that chunk
automatically. An indeterminate chunk invalidates the `MemAP`, but `Release`
remains available to restore its saved state. An accepted write is not replayed;
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

A complete Cortex-M identity composition acquires and releases state in one
of these orders:

```text
acquire: USB device → FTDI channel → debug port (enters SWD) → MEM-AP
release: MEM-AP → debug port → FTDI channel

acquire: USB device → J-Link session (configures SWD) → debug port → MEM-AP
release: MEM-AP → debug port → J-Link session

acquire: USB device → CMSIS-DAP session (connects SWD) → debug port → MEM-AP
release: MEM-AP → debug port → CMSIS-DAP session
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
- which explicit FTDI port, J-Link session, or CMSIS-DAP session and clock to
  request;
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
