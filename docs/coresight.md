# CoreSight component identity

`coresight.Identify` reads one component's identification registers through a
borrowed scalar-memory reader. A `dap.MemAP` implements that interface over
SWD or JTAG. The caller supplies the address of an accessible, 4 KiB aligned
identification page; the package does not find that address or walk ROM tables.

```go
component, err := coresight.Identify(ctx, memory, 0xe00ff000)
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
A directly acquired MEM-AP must be released before its debug port. Even these
identity reads temporarily change MEM-AP address and transfer state.

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
