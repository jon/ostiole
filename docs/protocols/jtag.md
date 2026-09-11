# JTAG

`jtag.Conn` owns the TAP state machine over a caller-supplied `jtag.Wire`.
It does not open or close an adapter. Give the connection exclusive use of
that wire, serialize calls, and keep the wire's owner alive until all calls
finish.

```go
conn := jtag.New(wire) // No traffic; wire belongs to its caller.
if err := conn.Reset(ctx); err != nil {
    return err
}
return conn.Move(ctx, jtag.Idle)
```

Reset clocks five TMS-high cycles; it does not assert a physical reset pin.
Move follows a shortest path through the IEEE 1149.1 state machine, driving
TDI low. Crossing update states can commit instructions or data, and TAP
reset can change debug state. The caller chooses which transitions are
appropriate for the target.

`ScanIR` and `ScanDR` capture and update a complete chain, then return to
Idle. They accept a packed input buffer and a positive bit count; unused
buffer bits are ignored. Each call starts a new capture, including when a
previous Move stopped in a shift or pause state. Raw scans must not bypass a
higher-level owner using the connection.

```go
captured, err := conn.ScanIR(ctx, []byte{0xff}, 8)
_ = captured
return err
```

That example shifts eight one bits. The caller must know the chain length
and what instruction those bits select on each device. `Idle(ctx, cycles)`
provides Run-Test/Idle clocks when the selected instruction needs them. A
zero-cycle request sends no traffic. Scans and idle requests are capped by
`MaxScanBits` (1,048,576); wire limits still apply to each physical transfer.

`Discover(ctx, maxTAPs)` resets the chain and returns detached reset-register
observations, nearest TDO first. Each observation is either an IDCODE or an
explicit one-bit bypass entry; it says nothing about the instruction-register
length. The bound must be between 1 and 1,024 TAPs. Discovery shifts ones and
recognizes an all-one terminator. An empty path and TDO stuck high both return
`ErrNoChain`; an overlong chain and TDO stuck low return `ErrDiscoveryLimit`
with the observed prefix. A partial inventory is not a validated chain.

`MeasureIR(ctx, maxBits)` measures the total instruction-chain length, not
the individual TAP lengths. After filling the register with ones, it shifts
a single zero marker through and measures its delay. It finishes in
BYPASS/Idle when the actual length fits the supplied bound (2 through 65,536
bits). A wrong bound or interrupted transfer can leave other instructions;
the caller must account for that effect when inspecting an unknown chain.

## Explicit layouts

`NewChain(conn, layout)` copies a nonempty layout in scan-out order (nearest
TDO first). Build each entry with `IDCODE(irBits, id)` or `Bypass(irBits)`;
the zero specification is invalid. IR lengths must be 2 through 64 bits.
IDCODEs match exactly, including revision bits. Bypass entries carry no
physical identity guarantee, and equal IDCODEs do not identify individual
devices.

```go
first, err := jtag.IDCODE(4, firstID)
if err != nil {
    return err
}
second, err := jtag.IDCODE(12, secondID)
if err != nil {
    return err
}
chain, err := jtag.NewChain(conn, jtag.Layout{first, second})
if err != nil {
    return err
}
if err := chain.Connect(ctx); err != nil {
    return err
}
cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
defer cancel()
return chain.Release(cleanupCtx) // Retain chain and wire if this fails.
```

Connect resets the chain, validates reset-register observations, measures
the total IR length, then checks the low `01` capture bits at each supplied
IR boundary. It leaves the chain in BYPASS/Idle. It uses the supported maximum
total IR length (65,536 bits) as its measurement bound, so validation clocks
more than 131,072 cycles even on a short chain. Give its context enough time
for that work at the selected adapter clock.

This checks the supplied layout, not its provenance: coincidental `01` bits
can make more than one boundary assignment plausible. Obtain individual IR
lengths from the device specification, not from IDCODE enumeration. Connect
does not enable hidden TAPs or perform board-specific configuration.

Release parks the chain in BYPASS/Idle, without restoring inherited
instructions or closing the wire. After lost state it validates the same
layout before attempting cleanup. Retain the chain and its wire owner after
a release error so cleanup can be retried with a fresh bounded context.
Successful release is idempotent. A failed validation leaves no validated
chain; the raw connection and wire remain the caller's responsibility.

## Wire transfers

The wire packs the earliest TMS, TDI, and TDO bit into bit zero of byte zero.
Each call clocks exactly the requested number of cycles. A wire can advertise
a positive `MaxTransferBits` limit; the connection splits longer movements
without inserting cycles. Without that interface, calls are capped at 4,096
bits. An invalid response length or a wire error loses TAP synchronization.
Only an explicit successful Reset permits movement afterward. Cancellation
before a wire call preserves the last confirmed state.

Hardware-independent tests exercise the wire and TAP state machine. No
bundled adapter implements `jtag.Wire` yet, so this path has not been tested
on hardware.

The [OpenOCD JTAG primer](https://openocd.org/doc/doxygen/html/primerjtag.html)
describes TAP state and scan mechanics. The reset sequence also appears in
the [Arm Debug Interface specification](https://documentation-service.arm.com/static/5f900a61f86e16515cdc0610).
