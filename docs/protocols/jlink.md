# J-Link USB, SWD, and JTAG

The `jlink` package implements the smallest J-Link application path needed to
provide `swd.Wire` and `jtag.Wire`. The command grammar and conservative host
rules come from Jon Olson's [independent J-Link over USB reference, edition
1.0][reference]. The reference draws on public sources and bounded
experiments; it is not SEGGER documentation. This note records the
implementation boundary and the physical observations behind host choices
which the reference leaves open.

[reference]: https://jon.dev/traces/j-link-over-usb/versions/1.0

Ostiole takes the command and transport rules it implements from `core-host`
claims in the reference. Ostiole's exact candidate catalog also includes the
additional PIDs recorded from the pinned libjaylink revision; those remain
discovery candidates rather than a product-family rule, and `Open` still
requires the active descriptors to match. Ostiole narrows descriptor binding
to `ff/ff/ff` with exactly two bulk endpoints. The firmware-scoped sample
correction described below applies the corresponding `observed-extension` only
under its exact product and record guard. The package emits no `research-only`
operation.

## Session boundary

Discovery uses a reviewed list of SEGGER application PIDs. Opening then finds
exactly one active `ff/ff/ff` alternate with one bulk IN and one bulk OUT
endpoint. USB owns descriptor parsing and transfers; `jlink` owns the command
stream, capability gates, interface selection, target clock, scan framing,
and probe status. `swd` owns SWD request and response grammar; `jtag` owns TAP
state, chain validation, and selected-TAP scans.

A metadata-only open sends version and capability-gated metadata queries but
does not select a target interface. `WithSWD` or `ConfigureSWD` selects
advertised interface 1; `WithJTAG` or `ConfigureJTAG` selects advertised
interface 0. Both then request the target clock. The package reports
the requested whole-kHz rate. The clock operation has no application response,
so it does not prove that a target can sustain the rate. The package does not
request adaptive clocking.

Combining SWD and JTAG options in one open fails before traffic. Repeating an
option for the same protocol uses the last clock ceiling. `SWDIO` and `JTAGIO`
require the corresponding configured interface; neither switches it implicitly.
Release a live protocol connection before reconfiguring the session. The
generic `Probe` owner permits only one activation and requires a fresh owner
to select a different protocol.

Only one operation may be outstanding. A known-length response may span
several USB completions. Surplus bytes from a completion are retained only for
the following response phase of that operation; bytes left after the final
phase poison the session. One zero-length packet is tolerated while reading a
known response; continued lack of progress, an invalid count, cancellation
after a command is sent, or another USB failure poisons the session. The
returned error preserves the original failure. Recovery is close, reopen, and
explicit reconfiguration; commands are never replayed.

## Scan v3

The request is command `0xcf`, reserved byte zero, a little-endian bit count,
then two packed streams. In SWD these are direction and output; in JTAG they
are TMS and TDI. Bits are least-significant first. SWD clears output bits for
target-driven cycles; JTAG preserves TDI independently of TMS and returns TDO
without sample shifting. Neither method changes the caller's buffers.
It reads the packed sample bytes and trailing status as distinct response
phases. Status zero succeeds; status 6 reports insufficient probe workspace.
A complete nonzero status leaves USB framing known but clears protocol
configuration, so a caller must configure again.

The default ceiling is 504 bits, and a reported workspace can lower it. USB
packet size does not lower the scan ceiling: the USB layer preserves full,
short, and zero-length completions, while the command stream retains any
coalesced status byte for the following response phase. `swd.Batch` can place
nine 54-bit overrun frames in one 486-bit scan without teaching `jlink` about
SWD transactions.

JTAG accepts an empty call without sending a command. A reported workspace
must accommodate at least eight clocks for JTAG; SWD retains its 136-bit
minimum connection sequence. The JTAG layer splits longer movements and scans
at the supplied limit. No JTAG-to-SWD selection, TAP movement, physical reset,
or target-assist command is hidden in JTAG configuration.

## Using JTAG

For a directly opened USB device, select JTAG during open:

```go
session, err := jlink.Open(ctx, device, jlink.WithJTAG(100_000))
if err != nil {
    return errors.Join(err, device.Close())
}
// Retain session until all JTAG cleanup and session.Close succeed.
conn := jtag.New(session)
```

Alternatively, open without options and call `session.ConfigureJTAG(ctx,
100_000)`. Configuration failure leaves the session available for `Close`.
For registered discovery, import `jlink/discovery` and activate the selected
probe's borrowed wire:

```go
owner, err := discover.OpenProbe(ctx, selector)
if err != nil {
    return err
}
wire, err := owner.JTAG(ctx, probe.JTAGConfig{MaxClockHz: 100_000})
if err != nil {
    return errors.Join(err, owner.Close())
}
conn := jtag.New(wire)
```

Both constructors leave TAP state unknown until explicit reset or discovery.
Use an explicit `jtag.Layout` for selected-TAP operations. Retain the chain and
release it with an independent bounded cleanup context before closing the
session or probe. Failed cleanup remains retryable; do not discard its owner.
Starting session close invalidates scans even if USB release needs a retry.
See [JTAG](jtag.md) for the full chain lifecycle and cleanup example.

## Bench observations

A J-Link EDU Mini V2 running firmware
`J-Link EDU Mini V2 compiled Jun 25 2026 10:27:52` was exercised at 100 kHz.
Its raw target-input samples were displaced by one target-driven clock across
scan boundaries. The package corrects that stream only for the exact observed
USB product and full firmware record. For an unrecognized firmware record, the
package returns the protocol sample bytes unchanged. This correction applies
only to SWD; JTAG samples are unchanged even on that firmware.

The initial target returned DPIDR `0x0BB11477`, SW-DP version 1, designer
`0x23B`, and AP0 IDR `0x04770021`. SW-DP version 1 does not use the SW-DPv2
multidrop selection mechanism. AP0 memory access returned CPUID `0x410CC200`
with part `0xC20`, identifying a Cortex-M0. Read-only composition through
`dap` and `MemAP` restored the saved AP0 CSW and TAR values before release. A
fresh J-Link session then returned the same DPIDR and CPUID with DHCSR.S_HALT
unchanged.

A second target on the same probe returned DPIDR `0x2BA01477`, AP0 IDR
`0x24770011`, and CPUID `0x410FC241` with part `0xC24`. Ten complete
restoration runs at a requested 100 kHz used twenty fresh J-Link sessions. In
every run the two sessions agreed on DPIDR and CPUID, DHCSR.S_HALT was
unchanged, and the saved AP0 CSW and TAR values were restored before release.
Each composition used 20 fixed frames in 48 SWDIO calls per session.

These observations establish the current path only. They do not generalize
the sample correction or transfer limit to other J-Link products, firmware,
USB speeds, or targets.
