# Cortex-M control

`target/cortexm.Identify` reads CPUID through any aligned-word reader. `Acquire`
additionally enables halting debug on Cortex-M0 or Cortex-M33 through a borrowed
`Memory`, whose `ReadWord` and `WriteWord` methods are supplied by `dap.MemAP`.
Other processor parts are rejected before a debug-register write.

## Ownership

Acquire sends target traffic but does not request a halt. It preserves an
inherited halt and rejects active stepping, interrupt masking, or an unfinished
halt transition. When debug is disabled, the halt, step, and interrupt-mask
control bits are unknown; acquisition initializes them to zero when enabling
debug.

The target requires exclusive control of the processor's debug registers. Do not
use another debugger or write those registers through raw memory while it is
acquired. Serialize the target and its memory connection. `Identity` returns the
cached CPUID after release; the zero target cannot access memory.

`Halt` waits for Debug state. `Resume` accepts only a halt requested by this
target. Observing an already-halted processor does not acquire permission to
resume it. `Halted` reads the current status without acquiring halt ownership.

Release the target before its MEM-AP or Arm debug owner. `Release` restores the
debug control changed by the target and leaves an inherited halt alone. The
caller controls operation cancellation and deadlines, including release. Without
either, an operation may wait indefinitely for the processor. Failed acquisition
attempts cleanup with a fresh five-second context; a non-nil target returned
with an error must be retained for release retries. Once release starts, or a
control write fails, ordinary target calls stop. Failed cleanup retains the
restoration state for another `Release`.

Cleanup needs a usable memory connection. A poisoned transport or invalidated
MEM-AP can prevent restoration; retaining the target does not repair either.
Retain both the target and its memory owner after a release failure so
restoration can be retried.

## Effects

Enabling halting debug changes how the processor handles debug events, even
before an explicit halt. Halting does not stop peripheral clocks. Resuming can
execute instructions before a later failure is reported, and release cannot undo
those instructions or recover elapsed time.

A successful memory write does not prove that the halt request cleared. If
readback never shows it clear, cleanup stays pending without repeating resume:
an ignored write cannot be distinguished from an immediate new halt.

A completed resume can immediately encounter a new debug event. The target
reports that halt and never repeats the completed resume during cleanup. If
debug was initially disabled, observing a new halt prevents restoration until
the processor runs again. After an uncertain halt or resume write, cleanup
likewise refuses to resume an observed halt: the memory interface cannot
establish whether the failed write caused that stop. A later running observation
permits cleanup to continue. On Cortex-M33, the target also relinquishes halt
ownership when it observes sticky restart status, even if a new event has
already halted the core again. The package has no forced-resume escape hatch.
Debug events racing with restoration of disabled debug can still affect
execution.

If halt readback shows that the request was lost, the target relinquishes halt
ownership. Cleanup leaves an independent stop alone; restoring initially
disabled debug waits until the processor is running.

DHCSR reads consume sticky reset and instruction-retirement indicators, and
Cortex-M33 restart status. The package does not restore those indicators or
clear DFSR event flags.

The Cortex-M0 implementation follows Arm DDI 0419E, sections C1.5 and
C1.6.3–C1.6.5 of the [Armv6-M Architecture Reference Manual][armv6m]. It does
not implement reset, breakpoints, or watchpoints.

## Cortex-M33 control

Cortex-M33 acquisition requires Secure invasive debug permission, indicated by
DHCSR.S_SDE. Restricted Non-secure-only debug and implementations without the
Security Extension are not supported by this control path. Acquisition does not
unlock debug, write authentication settings, select a security bank, or change
the processor's security state. Permission must remain available for control and
restoration.

The target rejects C_SNAPSTALL on acquisition and stops control if it observes
that bit later. Arm requires a system reset after snap-stall before execution
can safely resume; clearing the bit is insufficient. Once observed, the target
will not automatically resume the processor. Reset and recovery from that state
remain outside this API.

`Acquire`, `Halt`, `Halted`, `Resume`, `Release`, `ReadRegister`,
`WriteRegister`, and `Step` support Cortex-M0 and Cortex-M33.

On RP2350, core 0 uses the ADIv6 MEM-AP at `0x2000`. Select it through the
existing Arm debug owner, then use the target composition below:

```go
ap, err := dap.APAt(0x2000)
if err != nil {
    return err
}
memory, err := connection.OpenMemAP(ctx, ap)
if err != nil {
    return err
}
```

This controls one processor and does not configure cross-core stopping or
coordinate shared memory. Inherited cross-trigger routing can still couple the
cores. Halting a processor does not stop DMA and peripherals. The M33 debug
semantics follow DHCSR, DCRSR, and DCRDR in Arm DDI 0553B.y, sections D1.2.33,
D1.2.34, and D1.2.39 of the [Armv8-M Architecture Reference Manual][armv8m], and
the [RP2350 datasheet][rp2350].

## Stepping

`Step(ctx)` performs one Cortex-M0 or Cortex-M33 architectural step from a halt
owned by the target. It returns halted with stepping disabled, retaining
ownership for another step, register access, or resume. It rejects a running
processor or an inherited halt, settles any pending register transfer before
launch, and uses the caller's context for cancellation and deadlines.

```go
if err := core.Step(ctx); err != nil {
    // Retain core and its memory owner for Release.
    return err
}
pc, err := core.ReadRegister(ctx, cortexm.PC)
```

Stepping does not change interrupt masking. An architectural step can enter an
exception handler instead of retiring an instruction. A breakpoint, watchpoint,
vector catch, or external halt can also interrupt it. The target checks DFSR
before launch and rejects any existing flags for those events; it preserves all
DFSR flags. After launch, it requires a fresh halt with the HALTED reason and no
competing event before claiming that stop. A competing stop returns an error and
remains unowned.

Once launch is attempted, any failure leaves only `Release` available. Release
never repeats the step. After a confirmed launch, it waits for a fresh halt
before clearing C_STEP; it does not change stepping control while running. A
failed write to clear C_STEP can be retried without restarting execution. An
unconfirmed launch, ignored step request, reset, changed debug control, or loss
of the completed halt can prevent automatic cleanup. A competing stop can
prevent restoring initially disabled debug until the processor runs again. On
M33, the step's own restart is expected while waiting for completion. After
observing the completed halt, any further restart prevents automatic cleanup,
even if the core has already halted again. Permission and snap-stall checks
apply throughout; before clearing C_STEP, the target checks that the completed
halt is still present. Retain both owners when release fails; this package
provides no forced cleanup operation.

Instructions, exception entry, elapsed time, and peripheral effects cannot be
undone. Behavioral tests cover immediate and delayed completion, competing
flags, cancellation, ignored writes, partial failures, and cleanup retries. The
[micro:bit step bench](#step-bench) and [RP2350 step bench](#rp2350-step-bench)
record physical instruction checks. M33 step semantics follow Arm DDI 0553B.y
B13.4.2 and D1.2.38–D1.2.39.

## Register reads

`ReadRegister` reads R0–R12, SP, LR, PC, XPSR, MSP, or PSP from a halted
Cortex-M0 or Cortex-M33 processor. SP selects the current stack pointer; MSP and
PSP select the main and process stacks. On M33, all three use the halted
security state. The API does not change that state or DSCSR's memory-mapped bank
selection, and does not expose explicit Secure/Non-secure register selectors,
stack limits, or floating-point registers. PC is the debug return address. An
inherited halt permits inspection without acquiring permission to resume.
Invalid `Register` identifiers, including zero, are rejected before memory
traffic.

```go
pc, err := core.ReadRegister(ctx, cortexm.PC)
```

Reads write DCRSR and replace DCRDR; these transfer registers are not restored.
The target waits for S_REGRDY before and after selecting a register, using the
caller's context. It does not require observing S_REGRDY clear, since a transfer
may finish before the first status read.

A failed transfer leaves only `Release` available. Release waits for any pending
transfer, including one found busy before selection, before resuming or
disabling debug. It never replays a selector write whose completion is
uncertain. A failed precondition or cancellation before selection leaves the
target usable when no transfer is pending. An error returns no register value.

Reset, loss of Debug state, or observed M33 restart during a pending transfer
prevents automatic cleanup, even if a later status read would show ready. The
target cannot prove that the original transfer completed. Retain both owners;
there is no forced cleanup operation for this state. These failures have
behavioral test coverage, not physical failure-injection evidence.

## Register writes

`WriteRegister` writes the same register set except XPSR, which is read-only.
SP, MSP, and PSP require word-aligned values; PC requires bit zero clear. PC
writes change the debug return address without changing Thumb state. Writing SP
changes whichever stack bank is active. The API rejects invalid identifiers and
values before traffic; it does not check whether an address is mapped or
suitable for the program.

```go
err := core.WriteRegister(ctx, cortexm.R4, 42)
```

A write stages DCRDR, selects the register, and waits for transfer completion.
An error after attempting to stage data leaves only `Release` available. If
selection was attempted, the register may have changed even when the call
returns an error. Release settles a pending transfer without replaying it.
Successful writes are intentional changes to processor state: release does not
roll them back, and resumed execution uses the changed values. An inherited halt
permits writes but still does not grant permission to resume.

## Composition

For a MEM-AP borrowed from `armdebug.Conn`, acquire the target and retain any
non-nil result before checking the error:

```go
core, err := cortexm.Acquire(ctx, memory)
// Retain core for Release even when err is non-nil.
if err == nil {
    err = core.Halt(ctx)
}
if err == nil {
    err = core.Resume(ctx)
}
cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
cleanupErr := core.Release(cleanupCtx)
cancel()
err = errors.Join(err, cleanupErr)
// Close the Arm debug owner only after target release succeeds.
// On failure retain both owners for a later cleanup attempt.
```

The Cortex-M0 [control example](../examples/simple/cortexm-control/main.go)
selects one probe and AP, halts, prints PC, SP, R0, and R4, resumes, then
releases the target before closing the connection. With `-step`, it also steps
once and prints the resulting PC. It requires explicit consent to control
execution:

```sh
go run ./examples/simple/cortexm-control \
  -provider cmsisdap -serial SERIAL -ap 0 -clock 1000000 -allow-control -step
```

Hardware-independent tests model DHCSR control and execution state, including
partial writes, canceled operations, ignored writes, failed cleanup, and retry.
They do not establish physical halt/resume behavior on a bench program.

## Independent RP2350 cores

One `armdebug.Conn` can lend core 0's MEM-AP at `0x2000` and core 1's MEM-AP at
`0x4000`. Acquire a separate `cortexm.Target` for each. Serialize all calls over
the shared connection, including memory reads, target operations, and cleanup.
Each target owns only its processor's debug state and halt requests. Halting one
target does not claim ownership of a stop on the other.

For an already-open Arm owner `c`, the caller supplies operation `ctx` and a
live `cleanupCtx`, including after cancellation:

```go
var cores [2]*cortexm.Target
var err error
for i, base := range []uint64{0x2000, 0x4000} {
    ap, e := dap.APAt(base)
    if e != nil {
        err = e
        break
    }
    memory, e := c.OpenMemAP(ctx, ap)
    if e != nil {
        err = e
        break
    }
    cores[i], err = cortexm.Acquire(ctx, memory)
    if err != nil {
        break
    }
}
if err == nil {
    err = cores[0].Halt(ctx)
}
if err == nil {
    var halted bool
    halted, err = cores[1].Halted(ctx)
    if err == nil {
        fmt.Printf("core 1 halted=%v\n", halted)
    }
}
if err == nil {
    err = cores[0].Resume(ctx)
}
var releaseErr error
for i := len(cores) - 1; i >= 0; i-- {
    releaseErr = errors.Join(releaseErr, cores[i].Release(cleanupCtx))
}
err = errors.Join(err, releaseErr)
if releaseErr == nil {
    err = errors.Join(err, c.Close())
}
// Retain targets and c if target cleanup fails; retain c if Close fails.
```

A failed acquisition can return a non-nil target, so store it before handling
the error. Release every retained target before closing its memory owner; a
failed release leaves that owner live for retry. A target acquired while its
processor is halted cannot resume that inherited halt. Existing cross-trigger
routing can couple stops: inspect the bench's routing before expecting
independent progress. This composition provides per-core control, without group
ownership, coordinated stopping, or a simultaneous snapshot.

## Hardware procedure

The opt-in integration test selects the CMSIS-DAP micro:bit with serial
`9900360140124e4500279015000000360000000097969901`, AP0, and a requested 1 MHz
clock. The nRF51 needs at least 125 kHz during debug activation after power-on;
see the [startup evidence](protocols/cmsisdap.md#nrf51-startup-clock). It
requires a known firmware program with an aligned 32-bit RAM counter incremented
by the CPU at least once per 200 milliseconds. The counter must not be updated
by DMA or another processor. Loading firmware is outside the test.

The [counter firmware](../target/cortexm/testdata/counter/README.md) supplies a
loop that increments the counter at `0x20000000`, with build instructions and a
separate programming procedure. Loading it replaces the target program and
resets the processor.

```sh
OSTIOLE_CORTEXM_HIL_CONTROL=1 \
OSTIOLE_CORTEXM_HIL_PROGRAM='program name and build identity' \
OSTIOLE_CORTEXM_HIL_COUNTER=0xRAM_ADDRESS \
go test -tags integration ./target/cortexm -run '^TestHILCortexM0Control$' -v
```

Two fresh sessions check counter progress before control, no progress during a
halt, and renewed progress after resume and after release from a second halt.
The test compares inherited debug-enable and halt status before closing the Arm
debug owner. It refuses an already-halted bench. These observations do not
establish peripheral behavior, register preservation, reset, stepping, or
restoration after a physical transport failure.

## Hardware evidence

On September 26, 2026, Nostalgia (macOS) completed two control sessions at 1 MHz
on the selected micro:bit, with Cortex-M0 CPUID `0x410cc200`. The counter image
had been programmed and verified with OpenOCD 0.12.0. Its Intel HEX SHA-256 was
`ee294cc06ab6e8228161b49506675b065c0148b26421cf1f83c8e45e35cd4e5d`. After a
physical replug, Ostiole's read-only test connected first at 1 MHz, then the
control test ran without any intervening OpenOCD session.

In both control sessions, the CPU counter advanced before acquisition, remained
unchanged across ten samples 20 milliseconds apart while halted, and advanced
after resume and after release from a second halt. The halted values were
`0x0d8abd3d` and `0x0db618ae`. DHCSR was `0x01000000` before acquisition and
after release in each session: debug was initially disabled, acquisition enabled
it, and release restored disabled debug with the processor running. Both target
releases and Arm debug owner closes completed.

An earlier pair of sessions at 100 kHz, after OpenOCD had activated the
interface, preserved initially enabled debug. Those sessions do not establish
startup at 100 kHz. The later 1 MHz run covers initially disabled debug on the
same board. Neither run verifies state after closing the Arm debug owner or
cleanup after a physical transport failure. Peripheral behavior, register
preservation, reset, and stepping are outside this test.

### Register bench

`TestHILCortexM0Registers` uses the same micro:bit, AP0, and 1 MHz clock. It
requires the exact counter image above and checks its vectors and instruction
words before acquiring the processor. A second gate authorizes register writes:

```sh
OSTIOLE_CORTEXM_HIL_CONTROL=1 \
OSTIOLE_CORTEXM_HIL_REGISTERS=1 \
OSTIOLE_CORTEXM_HIL_PROGRAM=sha256:ee294cc06ab6e8228161b49506675b065c0148b26421cf1f83c8e45e35cd4e5d \
go test -tags integration ./target/cortexm -run '^TestHILCortexM0Registers$' -count=1 -v
```

On September 26, 2026, both fresh sessions passed on Nostalgia with CPUID
`0x410cc200`. Each read R0–R12, SP, LR, PC, XPSR, MSP, and PSP while halted. R4
accepted `0x55aa55aa` and `0xaa55aa55`; SP, MSP, PSP, and PC accepted temporary
aligned values. SP and MSP aliased as expected for this firmware. The test
restored each written value and compared all 19 registers with the saved
snapshot before resuming.

The CPU counter remained unchanged across ten samples 20 milliseconds apart
after register restoration, then advanced after resume and release. DHCSR was
`0x01000000` before acquisition and after release in both sessions, with debug
disabled and the processor running. Both target releases and Arm owner closes
completed. If register restoration cannot be confirmed, the test retains both
owners without requesting resume.

These sessions exercised register transfers while halted, not execution using
the temporary PC or stack values. Writes to the other general registers and LR,
process-stack selection, inherited halts, and failure cleanup have behavioral
test coverage only. XPSR writes, stepping, reset, and state after Arm owner
close were not tested.

### Step bench

`TestHILCortexM0Step` uses the same micro:bit and verified counter firmware,
with a separate gate for stepping:

```sh
OSTIOLE_CORTEXM_HIL_CONTROL=1 \
OSTIOLE_CORTEXM_HIL_STEP=1 \
OSTIOLE_CORTEXM_HIL_PROGRAM=sha256:ee294cc06ab6e8228161b49506675b065c0148b26421cf1f83c8e45e35cd4e5d \
go test -tags integration ./target/cortexm -run '^TestHILCortexM0Step$' -count=1 -v
```

On September 26, 2026, two fresh sessions passed on Nostalgia through CMSIS-DAP,
1 MHz SWD, and AP0, with CPUID `0x410cc200`. Each checked twelve consecutive
steps through the counter loop:

- At PC `0xc6`, `adds r0, #1` advanced PC to `0xc8` and incremented R0, leaving
  RAM unchanged.
- At PC `0xc8`, `str r0, [r1]` advanced PC to `0xca` and copied R0 to the
  counter at `0x20000000`, leaving R0 unchanged.
- At PC `0xca`, the branch returned PC to `0xc6`, leaving R0 and RAM unchanged.

After every step, `Halted` confirmed Debug state with stepping and interrupt
masking disabled. The counter then remained unchanged across ten samples 20
milliseconds apart while halted, and advanced after resume. Each session halted
again, checked one further step, and released from that halt. The counter
advanced after release. DHCSR was `0x01000000` before acquisition and after
release in both sessions; initially disabled debug and running state were
restored. Both target releases and Arm owner closes completed. The control
example also completed a step with `-allow-control -step`.

Stepping's register and memory effects were intentional and were not rolled
back. The firmware disables configurable interrupts, so these runs do not
establish exception entry, competing debug events, sleeping instructions, or
failure cleanup on hardware. Those control failures have behavioral coverage;
state after Arm owner close was not measured.

[armv6m]: https://documentation-service.arm.com/static/5f8ff05ef86e16515cdbf826
[armv8m]:
  https://community.arm.com/cfs-file/__key/communityserver-discussions-components-files/471/DDI0553B_5F00_y_5F00_armv8m_5F00_arm.pdf
[rp2350]: https://datasheets.raspberrypi.com/rp2350/rp2350-datasheet.pdf

## RP2350 hardware procedure

`TestHILRP2350Control` selects J-Link EDU Mini V2 serial `000802011345`,
requests 1 MHz SWD, and opens core 0's ADIv6 MEM-AP at `0x2000`. It requires
CPUID `0x411fd210` and a known program whose aligned RAM counter is incremented
only by that core. An inherited halt fails the test without resuming it. The
[RAM counter](../target/cortexm/testdata/rp2350-counter/README.md) provides the
program and separate OpenOCD preparation instructions.

```sh
OSTIOLE_RP2350_HIL_CONTROL=1 \
OSTIOLE_RP2350_HIL_COUNTER=0x20040000 \
OSTIOLE_RP2350_HIL_PROGRAM=c20737e61153b272322548e8e6db5c420f0c148d6707ca4412c309f70415065a \
go test -tags integration ./target/cortexm -run '^TestHILRP2350Control$' -count=1 -v
```

On September 27, 2026, OpenOCD 0.12.0 loaded and verified the RAM counter on
Nostalgia's RP2350 bench. Two fresh Ostiole sessions observed progress before
acquisition, no counter changes during halt, and renewed progress after resume
and release from a second halt. Both restored initially disabled debug and
running state before closing the Arm owner; target release and owner close
succeeded. Two earlier sessions also preserved initially enabled debug.

These observations cover core 0 with Secure invasive debug permitted and
configurable interrupts disabled. Failure recovery, permission denial, and
snap-stall rejection are covered only by behavioral tests. The test does not
establish core-1 control, cross-core coordination, Non-secure-only debug, M33
register access or stepping. State after closing the Arm debug owner was not
measured. The RAM program remains running after the test; original execution
state is not recovered.

## RP2350 register bench

After preparing the
[core-0 RAM counter](../target/cortexm/testdata/rp2350-counter/README.md), run
the separately gated register test:

```sh
OSTIOLE_RP2350_HIL_CONTROL=1 \
OSTIOLE_RP2350_HIL_REGISTERS=1 \
OSTIOLE_RP2350_HIL_PROGRAM=c20737e61153b272322548e8e6db5c420f0c148d6707ca4412c309f70415065a \
go test -tags integration ./target/cortexm -run '^TestHILRP2350Registers$' -count=1 -v
```

The test checks CPUID and the counter instructions before acquisition. It
requires a running bench and, after halting, verifies Secure state, main-stack
selection, and a PC inside the loop. It reads all 19 exposed registers, writes
and restores R4, SP, MSP, PSP, and PC, then verifies the whole snapshot and
unchanged DSCSR before resuming. An unconfirmed register restoration retains the
owners without requesting resume.

On Nostalgia, two fresh sessions through J-Link EDU Mini V2 `000802011345` at 1
MHz and AP `0x2000` passed on RP2350 core 0, CPUID `0x411fd210`. R4 retained
both `0x55aa55aa` and `0xaa55aa55`; stack and PC writes read back and were
restored before execution. All 19 registers matched their saved values and DSCSR
remained `0x00030000`. The counter stayed unchanged while halted and advanced
after resume and release. DHCSR's debug-enable and halt status matched initially
disabled debug and running state; both target release and Arm owner close
succeeded.

This exercises Secure state on core 0. Non-secure stack selection, transfer
failures, restart during transfer, and cleanup failures have behavioral
coverage. No security-state switch or core-1 control was performed. Temporary
stack and PC values were not executed. State after Arm owner close was not
independently measured. The previously loaded RAM program remains running; flash
was untouched.

## RP2350 step bench

After preparing the
[core-0 RAM counter](../target/cortexm/testdata/rp2350-counter/README.md), run
the separately gated step test:

```sh
OSTIOLE_RP2350_HIL_CONTROL=1 \
OSTIOLE_RP2350_HIL_STEP=1 \
OSTIOLE_RP2350_HIL_PROGRAM=c20737e61153b272322548e8e6db5c420f0c148d6707ca4412c309f70415065a \
go test -tags integration ./target/cortexm -run '^TestHILRP2350Step$' -count=1 -v
```

The test checks CPUID and counter instructions before acquisition and refuses an
inherited halt. After halting, it checks Secure state, Thread mode, Thumb state,
and R1's counter address. It compares PC, R0, and RAM after each step through
the increment at `0x20040026`, store at `0x20040028`, and branch at
`0x2004002a`. Twelve steps precede resume; a further halt and step exercise
release from an owned stop. DSCSR is compared across the first twelve steps. The
test does not reload firmware or roll back execution.

On Nostalgia, two fresh sessions through J-Link EDU Mini V2 `000802011345` at 1
MHz and AP `0x2000` passed on RP2350 core 0, CPUID `0x411fd210`. Each checked 13
steps, with PC/R0/RAM matching the expected instruction effects. The counter
stayed unchanged while halted and advanced after resume and release. DSCSR
remained `0x00030000`. Initially disabled debug and running state were restored
before Arm owner close; target release and owner close succeeded.

The RAM program disables configurable interrupts and runs in Secure state.
Exception entry, competing debug events, permission loss, snap-stall, restart
after a completed halt, and failure cleanup have behavioral coverage. These
sessions do not establish sleeping-instruction behavior, Non-secure execution,
core-1 control, or cross-core coordination. State after Arm owner close was not
independently measured. Flash was untouched and the counter remains running.

## RP2350 independent-core bench

`TestHILRP2350IndependentCores` selects J-Link EDU Mini V2 `000802011345` at 1
MHz, opens one Arm owner, and borrows AP `0x2000` and AP `0x4000`. Prepare the
[separate RAM counters][dual-counter] first. That procedure replaces both cores'
volatile execution state, resets core 1, and disables its Secure MPU for RAM
entry; it does not change flash or CTI routing.

```sh
OSTIOLE_RP2350_HIL_DUAL_CORE=1 \
OSTIOLE_RP2350_HIL_PROGRAM=bf878b47815bc5eaf6afb5279efaa6ff163832bd178c5b7b1604f80e6ad6cde9 \
go test -tags integration ./target/cortexm -run '^TestHILRP2350IndependentCores$' -count=1 -v
```

The test requires the known instruction words, running Secure counters, and
inactive CTIs before acquiring either target. It checks CTI architecture and
geometry, disabled control and integration mode, zero application triggers,
output and input-channel status, and all eight input/output routes. It reads and
preserves CTIGATE. It never writes routing or acknowledgements.

On Nostalgia, two fresh sessions read all 19 registers on each core and checked
one architectural step per core against PC, R0, and its counter word. Core 0's
counter stayed unchanged during its halt and after its step while core 1
advanced; the reverse held when core 1 was halted and stepped. Both counters
stopped after sequential halt requests. Resuming only core 0 left core 1
stopped; release from an owned halt restored progress on each core.

Both sessions restored initially disabled halting debug and running state on
both cores before Arm owner close, with DHCSR `0x01100000` before/after and
DSCSR `0x00030000` unchanged. Both CTIs remained disabled and unrouted, with
zero pending output/input-channel status and CTIGATE `0x0f` unchanged. Both
targets released and the shared owner closed successfully.

This covers the prepared Secure counter programs and serialized per-core
operations. It does not establish simultaneous stopping, CTI propagation,
Non-secure execution, sleeping instructions, or cross-core failure recovery.
Per-target cleanup failures have behavioral coverage; this bench does not inject
failures. State after Arm owner close was not independently measured. Both loops
remain running; preparation and instruction effects are not undone.

[dual-counter]: ../target/cortexm/testdata/rp2350-dual-counter/README.md
