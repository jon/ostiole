# Arm Debug Access Port

The Debug Access Port (DAP) is the register fabric behind an Arm debug port.
SWD supplies an AP/DP selector, two address bits, and a read/write bit;
`SELECT` turns that small window into DP register banks and a set of Access
Ports (APs). The surprising parts are the posted AP pipeline, power
handshakes, and the amount of state a supposedly read-only memory inspection
can disturb.

Arm [IHI 0031H, _Arm Debug Interface Architecture Specification ADIv5.0 to
ADIv5.2_](https://developer.arm.com/documentation/ihi0031/h) is the normative
ADIv5 specification. Use chapters B2, B4, C1, and C2 for the programmer's
model and requirements. This note keeps only the traps worth having close at
hand and the hardware observations below. “DAP” here means Arm Debug Access
Port, not Microsoft's Debug Adapter Protocol.

## The SW-DP register window

IHI 0031H sections B2.1-B2.2 and C1.2 define DP and AP addressing. These are
the bank-zero DP registers used by an ordinary ADIv5 connection:

| Offset | Read | Write |
| ---: | --- | --- |
| `0x00` | DPIDR | ABORT |
| `0x04` | CTRL/STAT | CTRL/STAT |
| `0x08` | RESEND | SELECT |
| `0x0c` | RDBUFF | - |

`SELECT.DPBANKSEL` changes the banked DP register at `0x04`.
`SELECT.APSEL` chooses an AP and `SELECT.APBANKSEL` supplies AP address bits
`[7:4]`; the two address bits in the following AP transaction choose one of
four registers in that bank.

DPIDR bit 0 is always one. Bits `[15:12]` identify the DP architecture version.
ADIv5 SW-DP defines DPv1 and DPv2; reading a structurally valid DPIDR does not
by itself make an unfamiliar version compatible.

Every AP implements IDR at offset `0xfc`, and IDR zero means that no AP is
present. IDR is read-only. Selecting a missing AP returns zero on reads and
ignores writes. The specification does not say that AP numbers are contiguous,
so stopping an AP scan at the first zero is a convenience, not an enumeration
rule.

The other register names and effects depend on the AP class. A MEM-AP DRW read
can update TAR when CSW.AddrInc is enabled. A write to the same register can
write target memory. Calling either operation a raw register transfer does not
make it harmless.

## Power and sticky state

IHI 0031H section B2.3 defines power control. The four high bits of CTRL/STAT
form two request/acknowledgement pairs:

| Bit | Name |
| ---: | --- |
| 28 | CDBGPWRUPREQ |
| 29 | CDBGPWRUPACK |
| 30 | CSYSPWRUPREQ |
| 31 | CSYSPWRUPACK |

If system power is requested, debug power must be requested with it; asserting
system power alone is UNPREDICTABLE. A host asserts the request bits, waits for
both acknowledgement bits, and only then starts AP transfers. When releasing
power, it clears its requests and waits for the acknowledgements to clear
before asking again.

The falling acknowledgement has a narrower meaning than it first appears to:
it says that the power controller accepted the request to remove power. It
does not prove that the domain is now off. Another requester may be keeping it
alive.

Record newly requested power bits as owned before writing them. If the write
then fails, the host cannot know whether the DP applied those bits. Bounded
rollback can attempt to re-enter SWD and clear only the bits which were absent
before acquisition.

Read CTRL/STAT before the first request which can stall. If ORUNDETECT is
already set, WAIT and FAULT have the overrun-detection data phase; use that
response grammar until the bit is cleared. There is an annoying bootstrap
problem: CTRL/STAT shares offset `0x04` with other DP banks, and a line reset
does not reset every DP register. After inheriting unknown DP state, read
DPIDR, clear the supported sticky conditions with ABORT, write zero to SELECT
once without retrying, read RDBUFF, then read CTRL/STAT at `0x04`. ABORT and
RDBUFF are bank-independent. ABORT first keeps inherited sticky state from
faulting SELECT; RDBUFF then shows whether the SELECT data took effect. If
either SELECT or RDBUFF returns WAIT or FAULT, the host cannot know which
response grammar it just received. Re-enter SWD before trying the bootstrap
again. Once another DP bank is selected, a read at `0x04` is no longer
CTRL/STAT and may legitimately return WAIT.

The SELECT write's OK acknowledgement comes before its data. There is no
second acknowledgement after the parity bit, so the host cannot trust the new
SELECT value until later traffic shows whether the write data took effect.
DPIDR and ABORT do not settle that question because sticky state cannot make
them return FAULT. RDBUFF is bank-independent and can settle it without relying
on the requested bank. WDATAERR means that the DP might have abandoned the
write and kept the previous bank. Do not use `0x04` until RDBUFF has returned
OK.

ABORT at DP offset `0x00` clears sticky conditions. On a full DP, writing
`0x1e` clears STICKYCMP, STICKYERR, WDATAERR, and STICKYORUN without setting
bit 0, DAPABORT. A Minimal DP does not implement pushed-compare operations;
its STKCMPCLR bit is reserved and the corresponding clear mask is `0x1c`.
Arm reserves DAPABORT for an AP transaction which has returned WAIT for an
extended period. Clearing all five bits with `0x1f` is not an equivalent
tidying operation.

## Posted AP transactions

IHI 0031H sections B4.2.2 and B4.2.7 define posted reads and write buffering.
AP reads are posted. The first AP read starts the access but returns an unknown
data value. A following AP read returns the previous result while starting
another access. Reading DP RDBUFF returns the final result without starting a
new AP access:

```text
AP read A       -> discard returned data
AP read B       -> result of A
DP RDBUFF read  -> result of B
```

A DP read other than RDBUFF can occur after the AP request without destroying
the pending result. An AP write or DP write does destroy it; a later AP read or
RDBUFF then returns an unknown value. This is one of the cases where “the next
transfer” is too vague to be useful.

Writes can also be buffered. An OK acknowledgement means that the DP accepted
the write, not that every earlier write has completed. An AP read, or a DP
operation which the DP is allowed to stall, drains the write buffer. DPIDR and
CTRL/STAT reads and ABORT writes are exceptions: the DP must not stall them,
and using one too early can abandon buffered writes and set WDATAERR. A
RDBUFF read is therefore a useful completion barrier after AP writes.

WAIT applies to the physical request which received it. If SELECT returns
WAIT, repeat SELECT. If the AP request returns WAIT, repeat that AP request.
Once the AP request returns OK, however, it has been accepted. If the following
RDBUFF read returns WAIT, repeat RDBUFF; repeating the AP request would start
the access twice. The same distinction matters for writes even when writing
the same value twice happens to look harmless.

With ORUNDETECT set, the WAIT response also sets STICKYORUN. Clear that sticky
condition through ABORT before retrying the WAITed request. A later FAULT in a
fixed response frame can mean that the DP abandoned a request after the
overrun; it is not permission to guess that the request ran. If SELECT was still
buffered when the WAIT arrived, the ABORT used to clear STICKYORUN can abandon
that write. Settle SELECT through RDBUFF before sending an AP or banked-DP
request, rather than guessing which selection survived after cleanup.

Changing ORUNDETECT has the same write-data boundary as SELECT. Keep using the
old response grammar through a following RDBUFF read. An OK acknowledgement
proves the CTRL/STAT write took effect even if the unused RDBUFF data has bad
parity; WDATAERR means it did not. RDBUFF can return WAIT while the write
remains buffered, in which case the host repeats RDBUFF under the old grammar.
Only a completed barrier lets the host choose the new grammar without guessing
which framing the target expects.

That replay rule assumes that each WAIT response finished cleanly. If the wire
fails during the following turnaround, the host no longer knows whether the DP
reached the next request header. If a later retry fails, the host might also
not know whether the AP access was accepted. DAPABORT is itself a normally
framed DP write, so it cannot repair unknown SWD framing. Abandon AP-derived
state and re-establish SWD before sending more requests.

“More requests” includes cleanup. Restoring AP registers or releasing power
still uses ordinary framed DP and AP traffic. Re-enter SWD first, or stop
cleanup before it sends a packet header.

After an AP transaction has returned WAIT for an extended period, DAPABORT
discards the outstanding AP transaction and any pending read result. The AP's
state is then UNPREDICTABLE. DAPABORT does not clear the sticky flags, and an
abandoned buffered write can leave WDATAERR behind. The host must clear
supported sticky flags separately. Once recovery reaches DAPABORT, the
interrupted AP operation has no safe high-level replay point.

DAPABORT makes the whole AP state unpredictable, including registers already
restored during cleanup. If it interrupts restoration, retry every saved AP
register, not just the one that returned WAIT.

Re-entering SWD repairs the request boundary; it does not repair AP state.
Restore saved AP registers before releasing debug power or disconnecting.
Successful cleanup does not make an invalidated AP handle usable again.

Post-abort repair is ordered traffic, not a best-effort checklist. If the
CTRL/STAT read or sticky-clear write fails, SWD framing might be unknown; do
not follow it with SELECT. Re-establish framing before sending another
request.

FAULT is different again. It reports sticky state and must not be replayed as
if it were WAIT. CTRL/STAT identifies the recorded conditions; an ABORT write
clears the supported ones. Read CTRL/STAT again before resuming ordinary
traffic: ABORT has no acknowledgement after its data phase either. WDATAERR
describes a write which the DP abandoned, while STICKYERR records an error
reported by an AP. They are not interchangeable names for the same failure.

A FAULT acknowledgement on an AP write means that write did not execute.
WDATAERR at the following completion barrier likewise records abandoned write
data. Other failures during AP work can leave its effects uncertain. Any AP
state which might have changed still has to be read or restored explicitly
before power is released.

A complete FAULT response after one or more WAITs still ends at a request
boundary. Return FAULT; the preceding WAITs do not justify DAPABORT or framing
invalidation.

## MEM-AP

IHI 0031H chapter C2 is the MEM-AP programmer's model. An ADIv5 MEM-AP reports
class `0b1000` in IDR. A single-word memory access uses three registers:

| Register | Offset | Purpose |
| --- | ---: | --- |
| CSW | `0x00` | Access size, address increment, and bus attributes |
| TAR | `0x04` | Target address |
| DRW | `0x0c` | Data access at TAR |

CFG at `0xf4` supplies three details needed before widening that operation:
BE selects the legacy big-endian byte-lane mapping, LA adds TARHI at `0x08`,
and LD advertises the Large Data Extension. LA widens the target address; it
does not widen TAR itself. When LA is set, save and restore TARHI along with
TAR.

IHI 0031H chapter C2 defines the CSW.Size encodings and the DRW byte lanes.
Only 32-bit access is mandatory. When an unsupported size is written, CSW
reads back a size the AP does support; check that value before accessing DRW.
For 8- and 16-bit transfers, the value does not always occupy the low bits of
DRW: the address and CFG.BE select its lane. Sixty-four-bit access also needs
CFG.LD and a CSW.Size readback of `0b011`. It uses two consecutive DRW
accesses, low word first and then high word. Until the second access completes,
only CSW and DRW may be accessed; a CSW access terminates the sequence. An
address above 32 bits needs CFG.LA and TARHI; the two extensions are
independent.

For one scalar, set CSW.Size, disable address increment, write TAR (and TARHI
when present), then read or write DRW. A DRW read is still an AP read, so its
value comes back through the posted pipeline. A DRW write must still complete
through RDBUFF before its effect can be attributed.

Automatic address increment is guaranteed only across TAR bits `[9:0]`.
Whether it crosses a 1 KiB boundary is implementation-defined in ADIv5. A
portable block implementation therefore ends each incrementing run and
reprograms TAR at the boundary. It cannot assume TAR advances linearly across
the boundary because it worked for the first kilobyte. The host also has to
read CSW back after requesting single address increment. If the setting does
not stick, it can disable increment and write TAR before every word.

Posted reads expose no result until a later AP read or RDBUFF. Buffered writes
expose no per-write completion point before a draining operation. A failed
completion therefore leaves the last read undelivered and the affected writes
ambiguous; replaying those writes can duplicate effects.

Even a memory read can change debug-side state: SELECT, CSW, TAR, TARHI, and
the DAP power requests. Saving and restoring that state matters when another
debugger, a ROM monitor, or later code expects to find it intact. A write has
the additional and much less subtle effect of changing the target memory the
caller selected.

## Bench note, 2026-08-09

The [SWD bench note](../protocols/swd.md) records the host, wiring, and uncached
command for this FT232H run. At a requested 400 kHz, it reached AP0 and one
Cortex-M system register:

```text
DPIDR  = 0x2ba01477
AP0 IDR = 0x24770011
AP0 CSW = 0x23000040
CPUID   = 0x410fc241
```

On 2026-08-22, the same bench read DPIDR, AP0 IDR, and AP0 CSW in one queued
transaction. The nine underlying SWD requests used two probe calls: one for
DPIDR and one containing the remaining eight fixed frames. All nine responses
were OK. The target did not produce a WAIT, so recovery from a partially
abandoned queue remains a simulator result.

AP0's IDR has class `0b1000`, identifying an ADIv5 MEM-AP. The run read CPUID
through CSW, TAR, and DRW, then verified that the saved CSW and TAR values were
restored. The debug and system power-request bits were zero both before and
after the connection.

The DAP test harness counted 85 OK acknowledgements and no WAIT, FAULT, or
invalid acknowledgements. The USB round trip between single transfers likely
gave this target ample time to finish ordinary AP work. The absence of WAIT
here is a bench observation, not evidence that replay and abort recovery are
correct.

On 2026-08-22 I let each SWD connection enable ORUNDETECT, then inverted the
data-parity bit of a same-value SELECT write in one test. The target set
WDATAERR and STICKYORUN, then returned FAULT on the next fixed-frame request.
A following CTRL/STAT read showed that WDERRCLR and ORUNERRCLR cleared both
sticky conditions, and the AP access completed. The seven DAP hardware tests
counted 194 OK acknowledgements, one FAULT, no WAIT, and no invalid
acknowledgements. Of those requests, 131 used fixed overrun-response frames.
Every debug-port release completed, including restoration of the inherited
ORUNDETECT setting. This exercises a target-generated sticky fault; it does
not exercise an AP-originated STICKYERR or a naturally occurring WAIT.

The same bench scanned APSEL 0 through 255 and found two nonzero identities:

```text
AP0 IDR = 0x24770011
AP1 IDR = 0x02880000
```

AP0 matched a separate identity read. The scan itself used 32 SWDIO calls for
1,022 fixed overrun-response frames, all with OK acknowledgements. It does not
demonstrate sparse numbering; the two implemented APs are adjacent.

A separately gated SRAM experiment saved 64 bytes at `0x20000000`, performed
8-, 16-, and 32-bit writes, and read the full range after each write. The
selected bytes changed and every neighboring byte retained its saved value.
The experiment then restored and verified all 64 original bytes. AP0 did not
advertise CFG.LD, so this run says nothing about physical 64-bit transfers. It
counted 3,130 OK acknowledgements, no WAIT, FAULT, or invalid acknowledgement,
and 3,122 fixed overrun-response frames. Cleanup released the MEM-AP and debug
port, then closed the FTDI channel.

```sh
OSTIOLE_FTDI_HIL=1 \
OSTIOLE_FTDI_HIL_WRITE=1 \
OSTIOLE_FTDI_HIL_SCRATCH=0x20000000 \
go test -count=1 -p 1 -tags=integration -v ./dap
```

A read-only run compared one 64-byte block read from the same range with 64
scalar byte reads and got the same bytes. The range was aligned to the start
of a TAR window, so it did not physically exercise boundary splitting. The
test counted 571 OK acknowledgements, no WAIT, FAULT, or invalid
acknowledgement, and 563 fixed overrun-response frames. Cleanup released the
MEM-AP and debug port, then closed the FTDI channel.

```sh
OSTIOLE_FTDI_HIL=1 \
OSTIOLE_FTDI_HIL_SCRATCH=0x20000000 \
go test -count=1 -p 1 -tags=integration -v ./dap \
  -run '^TestReadMEMAPBlockOverFTDI$'
```

That is enough to identify one working DP/AP/MEM-AP path and one physical
WDATAERR recovery path, and to demonstrate reversible scalar and block writes
to one known SRAM range. It says nothing yet about sparse APs, delayed power
acknowledgements, physical WAIT responses, auto-increment across 1 KiB, or
64-bit transfers. Those are better experiments than collecting more CPUID
values from the same board.

## ADIv6 is a different job

ADIv6 uses DPv3 and APv2. Arm
[IHI 0074F, _Arm Debug Interface Architecture Specification
ADIv6.0_](https://developer.arm.com/documentation/ihi0074/f) says directly
that DPv3 is not fully backward compatible with earlier DP versions; APv2 also
has a different common programmer's model and address space. Use IHI 0074F,
not this ADIv5 note, when implementing either one. Treating ADIv6 as a few
additional ADIv5 register constants would hide the actual compatibility
boundary.
