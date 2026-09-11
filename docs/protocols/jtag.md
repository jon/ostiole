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
