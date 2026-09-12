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

Coincidental `01` bits can satisfy more than one proposed set of IR
boundaries. Obtain individual IR lengths from the device specification, not
from IDCODE enumeration. Connect
does not enable hidden TAPs or perform board-specific configuration.

Release parks the chain in BYPASS/Idle, without restoring inherited
instructions or closing the wire. After lost state it validates the same
layout before attempting cleanup. Retain the chain and its wire owner after
a release error so cleanup can be retried with a fresh bounded context.
Successful release is idempotent. A failed validation leaves no validated
chain; the raw connection and wire remain the caller's responsibility.

## Selecting a TAP

After Connect, `chain.TAP(index)` lends a zero-based position. Its ScanIR
accepts exactly enough bytes for that TAP's instruction length, with unused
high bits zero, and puts every other TAP in BYPASS. Its ScanDR adds and removes
the other TAPs' bypass bits. Both return only the selected TAP's capture.

```go
tap, err := chain.TAP(0)
if err != nil {
    return err
}
if _, err := tap.ScanIR(ctx, []byte{0x0e}); err != nil {
    return err
}
id, err := tap.ScanDR(ctx, make([]byte, 4), 32)
```

This fragment reads the IDCODE instruction of an already-validated four-bit
Arm DAP. The caller still releases the chain using a fresh bounded context,
joins cleanup and operation errors, and keeps the wire's owner alive for any
cleanup retry. Do not assume that another device uses the same instruction.
`tap.Idle` supplies execution clocks without changing instructions.

A new Connect attempt or Release invalidates earlier borrowed TAPs. So does
a failed chain operation or raw traffic through the underlying Conn.
Revalidation never revives old TAP values. Selecting an instruction on a
different TAP requires selecting this TAP's instruction again before its
next data scan; `ErrInstructionChanged` reports that condition before traffic.
Keep exclusive use of the Conn and serialize all Chain and TAP calls.

## Wire transfers

The wire packs the earliest TMS, TDI, and TDO bit into bit zero of byte zero.
Each call clocks exactly the requested number of cycles. A wire can advertise
a positive `MaxTransferBits` limit; the connection splits longer movements
without inserting cycles. Without that interface, calls are capped at 4,096
bits. An invalid response length or a wire error loses TAP synchronization.
Only an explicit successful Reset permits movement afterward. Cancellation
before a wire call preserves the last confirmed state.

## FTDI wire

An FTDI probe opened through discovery or `ftdi.OpenProbe` can lend JTAG:

```go
wire, err := opened.JTAG(ctx, probe.JTAGConfig{MaxClockHz: 100_000})
if err != nil {
    return errors.Join(err, opened.Close()) // Retain opened if cleanup fails.
}
conn := jtag.New(wire)
```

Use that connection with the explicit layout above. Release the chain with
a fresh bounded context before calling `opened.Close`; retain both values
if chain release fails. A probe activates only one protocol. Close invalidates
the borrowed wire, even if the implementation still has cleanup to retry.

`ftdi.Open(ctx, device, ftdi.Config{Port: ftdi.PortA, MaxClockHz: 100_000})`
opens one explicitly selected USB attachment as a protocol-neutral MPSSE
channel. Opening leaves target pins as inputs; `JTAGIO` establishes JTAG
pin directions before each nonempty call. `SWDIO` establishes SWD directions.
The config selects the port and clock; the wire operation determines directions. A non-nil
`Channel` owns the attachment even when open returns an error; close that
channel and retain it if cleanup fails. A nil channel leaves the device with
the caller for cleanup. Opening does not preserve earlier FTDI settings.

The caller must verify standard MPSSE wiring: pin 0 TCK, pin 1 TDI, pin 2
TDO, and pin 3 TMS. TCK/TDI/TMS are outputs and the remaining pins are inputs.
No reset pin is driven. The channel drives TMS and TDI on falling TCK edges
and samples TDO on rising edges. Both wire methods accept at most 8,192 clocks per call;
`jtag.Conn` splits longer scans. A failed exchange poisons the channel rather
than replaying clocks. Release the chain before closing its channel. Do not mix raw SWD and JTAG
traffic underneath a protocol connection; serialize every call on the channel.

On Nostalgia, the ZCU104 FT4232H (`0403:6011`, serial `01691`, port A) passed:

```sh
OSTIOLE_ZCU104_JTAG_HIL=1 OSTIOLE_JTAG_SERIAL=01691 \
  go test -tags integration ./ftdi -run '^TestHILFT4232H(Probe)?JTAG$' -count=1 -v
```

At 100 kHz, reset discovery returned Arm IDCODE `0x5ba00477` and Xilinx
IDCODE `0x14730093`. The test validated the explicit IR4/IR12 layout, read
the Arm IDCODE through selected TAP 0, parked the chain in BYPASS/Idle, and
closed the channel. Both direct opening and registered discovery through
`Probe.JTAG` completed the same sequence. The board's DAP had already been
activated externally; neither path activates it or accesses DAP registers
or target memory.

The [OpenOCD JTAG primer](https://openocd.org/doc/doxygen/html/primerjtag.html)
describes TAP state and scan mechanics. The reset sequence also appears in
the [Arm Debug Interface specification](https://documentation-service.arm.com/static/5f900a61f86e16515cdc0610).
