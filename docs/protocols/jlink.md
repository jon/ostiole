# J-Link USB and SWD

The `jlink` package implements the smallest J-Link application path needed to
provide an `swd.Wire`. The command grammar and conservative host rules come
from Jon Olson's [independent J-Link over USB reference, edition
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
and probe status. `swd` continues to own SWD request and response grammar.

A metadata-only open sends version and capability-gated metadata queries but
does not select a target interface. `WithSWD` or `ConfigureSWD` selects
advertised interface 1, then requests the target clock. The package reports
the requested whole-kHz rate. The clock operation has no application response,
so it does not prove that a target can sustain the rate. The package does not
request adaptive clocking.

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
then packed direction and output streams. Bits are least-significant first.
The package clears output bits for target-driven cycles before sending them.
It reads the packed sample bytes and trailing status as distinct response
phases. Status zero succeeds; status 6 reports insufficient probe workspace.
A complete nonzero status leaves USB framing known but clears SWD
configuration, so a caller must configure again.

The default ceiling is 504 bits, and a reported workspace can lower it. USB
packet size does not lower the scan ceiling: the USB layer preserves full,
short, and zero-length completions, while the command stream retains any
coalesced status byte for the following response phase. `swd.Batch` can place nine
54-bit overrun frames in one 486-bit scan without teaching `jlink` about SWD
transactions.

## Bench observations

A J-Link EDU Mini V2 running firmware
`J-Link EDU Mini V2 compiled Jun 25 2026 10:27:52` was exercised at 100 kHz.
Its raw target-input samples were displaced by one target-driven clock across
scan boundaries. The package corrects that stream only for the exact observed
USB product and full firmware record. For an unrecognized firmware record, the
package returns the protocol sample bytes unchanged.

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
