# CoreSight component inspection

`coresight.Identify` reads one component's identification registers through a
borrowed scalar-memory reader. A `dap.MemAP` implements that interface over
SWD or JTAG. Obtain the advertised identification page from the selected
MEM-AP with `ReadDebugBase`, or supply an explicitly known address. The package
can also read individual ROM entries or walk a hierarchy with explicit limits.

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

## Reading ROM entries

`Component.ROMTable` derives entry geometry from an identification snapshot
without accessing memory. It recognizes class 1 and Arm's class 9 ROM
architecture `0x0af7`, revision 0. Other component architectures return
`ErrNotROMTable`; an unsupported ROM revision or entry format returns an error.
A zero table is invalid.

```go
table, err := component.ROMTable()
if err != nil {
    return err
}
for i := 0; i < table.EntryCount(); i++ {
    entry, err := table.ReadEntry(ctx, memory, i)
    if err != nil {
        return err
    }
    if entry.End {
        break
    }
    if entry.Present {
        fmt.Printf("entry=%d base=%#x power-ID=%d valid=%t\n",
            i, entry.Base, entry.PowerID, entry.PowerIDValid)
    }
}
```

Class 1 tables hold at most 960 32-bit entries. Class 9 DEVID.FORMAT selects
512 32-bit or 256 64-bit entries. A table that fills every slot needs no
additional terminator. `ReadEntry` reads both words of a 64-bit entry before
interpreting it and returns no partial entry on error. It applies signed
relative offsets without allowing address underflow or overflow.

The decoder follows IHI 0029E D6.4.4 and D7.5.17. It rejects reserved
formats, nonzero reserved bits, zero offsets in present entries, and nonzero
class 9 terminators. Class 9 absence (`PRESENT=2`) leaves the remaining bits
uninterpreted. Class 1 FORMAT=0 entries are unsupported; all-ones entries
are malformed. `Raw` retains the complete entry value on success. Unknown
class 9 architectures are not interpreted as tables based on their part
number alone.

Entry reads do not access the child. A valid power ID is scoped to the
containing table and does not establish that the child is powered. This API
does not request power. Callers must establish access before identifying a
child in another power domain. Reader ownership and cleanup remain as above.

## Bounded traversal

`Walk` identifies a root and follows present entries in depth-first order. A
root that is not a ROM table produces one successful visit. Limits apply to
the entire walk, with root depth zero. The component limit counts the root,
failed identities, and skipped power-domain children. The entry limit counts
absent entries and terminators as well as present entries. Validate limits
before opening hardware when they come from application arguments.

```go
limits := coresight.WalkLimits{MaxDepth: 8, MaxComponents: 256, MaxEntries: 4096}
if err := limits.Validate(); err != nil {
    return err
}
visits, err := coresight.Walk(ctx, memory, base, limits)
for _, visit := range visits {
    if visit.Component != nil {
        fmt.Printf("parent=%d entry=%d base=%#x class=%#x\n",
            visit.Parent, visit.Index, visit.Component.Base, visit.Component.Class())
    }
    if visit.Err != nil {
        fmt.Printf("parent=%d entry=%d: %v\n", visit.Parent, visit.Index, visit.Err)
    }
}
if err != nil {
    return err
}
```

Each visit refers to its parent by index in the returned slice. The root has
`Parent=-1` and `Index=-1`. Other visits retain the decoded entry, including
power metadata scoped to the parent table. `Component` is nil if the identity
was not obtained. Absent entries and terminators have no visits; use individual
entry reads when their raw values matter.

A power-domain child is recorded with `ErrPowerDomain` and skipped before any
child access. The walk continues through its accessible siblings but returns
a non-nil error, so those results cannot be mistaken for a complete inventory.
It does not test a power-control register, request power, or offer an option
to assume an advertised domain is accessible.

Other failures stop the walk immediately, including malformed entries,
unsupported ROM formats, repeated tables, exhausted limits, and memory errors.
Repeated table references include cycles and duplicate references from separate
parents; they fail before another identity read. Ordinary component references
may repeat. Unknown component architectures remain leaves. A successful walk
covers the supported tables reached from this root, not every debug component
in the system.

The returned error preserves underlying memory errors and matches
`ErrWalkLimit`, `ErrRepeatedTable`, or `ErrPowerDomain` when applicable. Earlier
visits remain available; an identity failure is recorded on its visit. An entry
read failure or exhausted limit is reported in the returned error, without a
child visit. Stop using a failed MEM-AP according to its recovery rules,
then release its owner with bounded, retryable cleanup.

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
The example defaults to a 1 MHz clock, accepts `-clock` in Hz, and applies a
ten-second operation deadline. The library also accepts memory clients reached
through JTAG; the example configures SWD only.

Add `-walk` to follow the advertised root with depth 8, at most 256 visits,
and at most 4096 entry reads across the hierarchy:

```sh
go run ./examples/simple/coresight-info \
  -provider cmsisdap -serial SERIAL -ap 0 -walk
```

`-base ADDRESS` also applies to walks. The output includes parent and entry
indexes, available identities, per-component errors, and a `complete` field.
Incomplete inspection exits unsuccessfully after printing its partial results
and attempting owner cleanup. These fixed bounds keep the example small;
library callers supply their own `WalkLimits`.

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

## ROM traversal hardware evidence

On September 12, 2026, Nostalgia ran:

```sh
OSTIOLE_ROM_HIL=1 \
  go test -tags=integration -run '^TestHILROMWalk$' -count=1 -v ./coresight
```

The test uses the same exact probe selections and externally enabled ZCU104
chain described above. In that run, each path opened two fresh sessions at
100 kHz, read its MEM-AP's advertised root, and walked with depth 8, 256
visits, 4096 entry reads, and a 120-second operation deadline.

The micro:bit SWD AP0 walk completed with six identities. Its root at
`0xf0000000` led to the nested table at `0xe00ff000`, components at
`0xe000e000`, `0xe0001000`, and `0xe0002000`, and a component at `0xf0002000`.
The `coresight-info -walk` example also reported six visits and `complete=true`
using that probe's exact serial and its ten-second deadline.

The ZCU104 JTAG AP1 walk was incomplete. From root `0x80000000`, it identified
sixteen children at `0x80100000` through `0x801f0000`, then stopped on a DAP
FAULT while reading CIDR at `0x803e0ff0`. The result retained those seventeen
identities and a failed eighteenth visit for root entry 16. The test checks
this access boundary; it does not count the inaccessible component as
identified or attempt later entries. The error does not distinguish a power
restriction from another cause of that target access fault.

Both sessions reproduced each bench's result. Every owner reported successful
close, including after the ZCU104 fault. This does not independently measure
restored state after close. No target-memory writes, component power requests,
unlocks, processor control, or board activation were performed. The observed
tables were class 1. Class 9 layouts and power-domain skips have ordinary
test coverage; large addresses and both memory byte orders also have public
MEM-AP simulation coverage. Those cases were not exercised on hardware.

## ADIv6 and the RP2350

The example accepts `-ap-base` for an ADIv6 MEM-AP, or `-debug-space` to
inspect the DP's own advertised tree. Choose exactly one of `-ap`,
`-ap-base`, and `-debug-space`. `-base` still overrides the root within the
selected address space.

```sh
go run ./examples/simple/coresight-info \
  -provider jlink -serial 000802011345 -debug-space -walk

go run ./examples/simple/coresight-info \
  -provider jlink -serial 000802011345 -ap-base 0x2000 -walk
```

On September 20, 2026, Nostalgia exercised the RP2350 through J-Link EDU Mini
V2 serial `000802011345`, with SWD requested at 100 kHz. Two fresh sessions
for each path used:

```sh
OSTIOLE_ARMDEBUG_HIL=1 \
  OSTIOLE_PROBE_HIL_PROVIDER=jlink \
  OSTIOLE_PROBE_HIL_SERIAL=000802011345 \
  OSTIOLE_ARMDEBUG_HIL_DEBUG_SPACE=1 \
  go test -tags=integration ./armdebug -run '^TestHILArmConnection$' -count=2 -v

for ap_base in 0x2000 0x4000; do
  OSTIOLE_ARMDEBUG_HIL=1 \
    OSTIOLE_PROBE_HIL_PROVIDER=jlink \
    OSTIOLE_PROBE_HIL_SERIAL=000802011345 \
    OSTIOLE_ARMDEBUG_HIL_AP_BASE="$ap_base" \
    OSTIOLE_ARMDEBUG_HIL_WALK=1 \
    go test -tags=integration ./armdebug -run '^TestHILArmConnection$' -count=2 -v
done
```

The integration walks used depth 8, 64 visits, 256 entry reads, and a
10-second deadline. The debug port reported DPIDR `0x4c013477`, DPIDR1
`0x94` (20 address bits), and a present discovery root at address zero. Its
class 9 ROM table led to six children, including MEM-APs at `0x2000` and
`0x4000` with DEVARCH `0x47700a17`. The walk completed with seven identities.

Both MEM-APs returned IDR `0x34770008`, CPUID `0x411fd210`, and debug base
`0xe00ff000`. Each target-memory walk completed with seven identities.
The example also completed all three paths with its existing depth 8,
256-visit and 4096-entry limits.

All connection cleanup calls returned successfully. Restored state was
not independently measured after close. These runs performed no target-memory
writes, halt, reset, component unlock, or component power requests. The walks
cover advertised entries, not every component in the RP2350 debug address
space. Addresses above 32 bits, memory writes, and injected failures have
simulation coverage but were not exercised on this bench.

On September 26, the micro:bit identity and ROM-walk tests repeated both
sessions at a requested 1 MHz and reproduced the identities and six visits
above. Both tests and the SWD example now use 1 MHz by default to meet the
nRF51's [startup clock requirement](protocols/cmsisdap.md#nrf51-startup-clock).
The ZCU104 test clock remains 100 kHz. Run only the micro:bit paths with:

```sh
OSTIOLE_CORESIGHT_HIL=1 OSTIOLE_ROM_HIL=1 \
go test -tags integration ./coresight \
  -run 'TestHIL(ComponentIdentity|ROMWalk)/microbit' -count=1 -v
```
