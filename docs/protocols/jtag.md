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
