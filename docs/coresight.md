# CoreSight component identity

`coresight.Identify` reads one component's identification registers through a
borrowed scalar-memory reader. A `dap.MemAP` implements that interface over
SWD or JTAG. Obtain the advertised identification page from the selected
MEM-AP with `ReadDebugBase`, or supply an explicitly known address. The package
does not walk ROM tables.

```go
base, present, err := memory.ReadDebugBase(ctx)
if err != nil {
    return err
}
if !present {
    return errors.New("MEM-AP advertises no debug entry")
}
component, err := coresight.Identify(ctx, memory, base)
if err != nil {
    return err
}
designer, jedec := component.Designer()
fmt.Printf("class=%#x part=%#x designer=%#x JEP106=%t\n",
    component.Class(), component.Part(), designer, jedec)
if architecture, present := component.Architecture(); present {
    fmt.Printf("architect=%#x architecture=%#x revision=%d\n",
        architecture.Architect, architecture.ID, architecture.Revision)
}
```

The reader remains borrowed throughout the call. When `memory` comes from
`armdebug.Conn.OpenMemAP`, close that connection and retry failed cleanup as
shown in [Composing Ostiole](composition.md#select-and-open-hardware-explicitly).
A directly acquired MEM-AP must be released before its debug port. Reading
BASE does not access target memory or change CSW/TAR; reading the component
identity temporarily changes MEM-AP address and transfer state. An advertised
address does not establish that the component is accessible. See
[MEM-AP debug base](ports/dap.md#mem-ap-debug-base) for presence and format
handling.

The register layout follows Arm IHI 0029E, sections B2.2 and B2.3 of the
[CoreSight Architecture Specification v3.0](https://documentation-service.arm.com/static/5f900a19f86e16515cdc041e).
The reader validates the CIDR preamble before reading PIDR. For class 9, it
also reads DEVARCH, DEVID, and DEVTYPE; it skips those registers for other classes.
The snapshot preserves unknown classes and parts. It exposes a DEVARCH
architecture only when PRESENT is set, and marks whether the PIDR designer
uses JEP106. A part number alone does not identify a component architecture.

`CIDR` and `PIDR` pack the low bytes in register-number order. Reserved upper
bits are ignored. `PIDR` retains REVAND, CMOD, and the encoded SIZE field;
SIZE is not treated as a reliable component extent. `Base` names the
identification page, which may differ from the beginning of a larger component.
The API accepts 64-bit addresses and uses numeric 32-bit scalar reads, leaving
byte order to the supplied memory reader.

Invalid arguments fail before traffic. A read failure returns a zero snapshot
with the failing register address and underlying error. Cancellation stops
further reads. Callers must not interpret failure as an absent component or a
complete inventory: this API cannot distinguish a lock, a power restriction,
and a transport failure unless the reader's error supplies that information.
A successful identity also does not establish access to functional registers.

Inspection performs no target-memory writes, unlocks, component power
requests, CTI configuration, halt, or reset. Callers must establish that the
address is safe to inspect and that required board routing and component power
are already available. DAP setup and restoration still have the effects
described in the [architecture guide](architecture.md#safety-effects).

Deterministic tests cover malformed identities, unknown classes, optional
fields, cancellation, failures at every register read, and the final aligned
page of the 64-bit address space. Composition tests exercise the existing
SWD/DAP simulator with both MEM-AP byte orders above 4 GiB.

## Inspection example

`examples/simple/coresight-info` opens a managed SWD connection, acquires the
required AP, obtains its debug base, reads that identification page, and
attempts owner cleanup up to three times. AP is required; probe filters may be
omitted only when selection remains unique. An absent debug entry produces an
error without attempting target-memory access.

```sh
go run ./examples/simple/coresight-info \
  -provider cmsisdap -serial SERIAL -ap 0
```

To inspect another known page, supply `-base ADDRESS`; this bypasses the BASE
read. The override must name an accessible, 4 KiB aligned identification page.
The example requests a 100 kHz clock and applies a ten-second operation deadline. The library also accepts memory clients reached
through JTAG; the example configures SWD only.

## Hardware evidence

On September 12, 2026, the macOS Nostalgia bench ran:

```sh
OSTIOLE_CORESIGHT_HIL=1 \
  go test -tags=integration -run TestHILComponentIdentity -count=1 -v ./coresight
```

Each path opened two fresh sessions at a requested 100 kHz. The test first
read and identified the MEM-AP's advertised entry, then read the known
component page below through the same client:

| Path | Advertised address | Result |
| --- | --- | --- |
| micro:bit SWD AP0 | `0xf0000000` | CIDR `0xb105100d`, PIDR `0x02007c4001`, class 1. |
| ZCU104 JTAG AP1 | `0x80000000` | CIDR `0xb105100d`, PIDR `0x0100193730`, class 1. |

The additional explicitly addressed reads returned:

| Path | Identification page | Result |
| --- | --- | --- |
| CMSIS-DAP v2 micro:bit, serial `9900360140124e4500279015000000360000000097969901`, SWD AP0 | `0xe00ff000` | CIDR `0xb105100d`, PIDR `0x04000bb471`, class 1, Arm part `0x471`. |
| FT4232H `01691`/A, ZCU104 JTAG AP1 | `0x80410000` | CIDR `0xb105900d`, PIDR `0x04004bbd03`, class 9, Arm part `0xd03`; DEVARCH `0x47706a15`, DEVID `3`, DEVTYPE `0x15`. |

Both sessions on each path returned the same identity. Every Arm debug owner
reported successful close, including its MEM-AP and DAP restoration and probe
release. This test does not independently measure restored state after close.
The ZCU104 used the externally enabled Arm `0x5ba00477`/IR4 and Xilinx
`0x14730093`/IR12 chain. No board routing, component unlock, halt, reset, or
target-memory write was performed.

The example passed on that micro:bit with its exact serial, both with the
advertised address and with `-base 0xe00ff000`. These results cover the
advertised entry and one known page on each bench, not ROM traversal,
component register access, or physical large-address and big-endian support.
