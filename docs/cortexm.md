# Cortex-M control

`target/cortexm.Identify` reads CPUID through any aligned-word reader.
`Acquire` additionally enables halting debug on Cortex-M0 through a borrowed
`Memory`, whose `ReadWord` and `WriteWord` methods are supplied by `dap.MemAP`.
Other processor parts are rejected before a debug-register write.

## Ownership

Acquire sends target traffic but does not request a halt. It preserves an
inherited halt and rejects active stepping, interrupt masking, or an unfinished
halt transition. When debug is disabled, the other control bits are unknown;
acquisition initializes them to zero when enabling debug.

The target requires exclusive control of the processor's debug registers. Do
not use another debugger or write those registers through raw memory while it
is acquired. Serialize the target and its memory connection. `Identity` returns
the cached CPUID after release; the zero target cannot access memory.

`Halt` waits for Debug state. `Resume` accepts only a halt requested by this
target. Observing an already-halted processor does not acquire permission to
resume it. `Halted` reads the current status without acquiring halt ownership.

Release the target before its MEM-AP or Arm debug owner. `Release` restores
the debug control changed by the target and leaves an inherited halt alone.
Each operation is bounded to five seconds or the caller's earlier deadline.
Failed acquisition attempts cleanup with a fresh five-second context; a
non-nil target returned with an error must be retained for release retries.
Once release starts, or a control write fails, ordinary target calls stop.
Failed cleanup retains the restoration state for another `Release`.

Cleanup needs a usable memory connection. A poisoned transport or invalidated
MEM-AP can prevent restoration; retaining the target does not repair either.
Retain both the target and its memory owner after a release failure so
restoration can be retried.

## Effects

Enabling halting debug changes how the processor handles debug events, even
before an explicit halt. Halting does not stop peripheral clocks. Resuming
can execute instructions before a later failure is reported, and release
cannot undo those instructions or recover elapsed time.

A successful memory write does not prove that the halt request cleared. If
readback never shows it clear, cleanup stays pending without repeating resume:
an ignored write cannot be distinguished from an immediate new halt.

A completed resume can immediately encounter a new debug event. The target
reports that halt and never repeats the completed resume during cleanup. If
debug was initially disabled, observing a new halt prevents restoration until
the processor runs again. After an uncertain halt or resume write, cleanup
likewise refuses to resume an observed halt: the memory interface cannot
establish whether the failed write caused that stop. A later running observation
permits cleanup to continue. The package has no forced-resume escape hatch.
Debug events racing with restoration of disabled debug can still affect
execution.

If halt readback shows that the request was lost, the target relinquishes
halt ownership. Cleanup leaves an independent stop alone; restoring initially
disabled debug waits until the processor is running.

DHCSR reads consume the sticky reset and instruction-retirement indicators.
The package does not restore those indicators or clear DFSR event flags.

The implementation follows Arm DDI 0419E, sections C1.5 and C1.6.3–C1.6.5 of the
[Armv6-M Architecture Reference Manual](https://documentation-service.arm.com/static/5f8ff05ef86e16515cdbf826).
It does not implement reset, single-step, breakpoints, or
watchpoints.

## Register reads

`ReadRegister` reads R0–R12, SP, LR, PC, XPSR, MSP, or PSP from a halted
processor. SP selects the current stack pointer; MSP and PSP select its banks.
PC is the debug return address. An inherited halt permits inspection without
acquiring permission to resume. Invalid `Register` identifiers, including
zero, are rejected before memory traffic.

```go
pc, err := core.ReadRegister(ctx, cortexm.PC)
```

Reads write DCRSR and replace DCRDR; these transfer registers are not restored.
The target waits for S_REGRDY before and after selecting a register, with the
same five-second bound as control operations. It does not require observing
S_REGRDY clear, since a transfer may finish before the first status read.

A failed transfer leaves only `Release` available. Release waits for any
pending transfer, including one found busy before selection, before resuming
or disabling debug. It never replays a selector write whose completion is
uncertain. A failed precondition or cancellation before selection leaves the
target usable when no transfer is pending. An error returns no register value.

Reset or loss of Debug state during a pending transfer prevents automatic
cleanup, even if a later status read would show ready. The target cannot prove
that the original transfer completed. Retain both owners; there is no forced
cleanup operation for this state. These failures have behavioral test coverage,
not physical failure-injection evidence.

## Register writes

`WriteRegister` writes the same register set except XPSR, which is read-only.
SP, MSP, and PSP require word-aligned values; PC requires bit zero clear. PC
writes change the debug return address without changing Thumb state. Writing
SP changes whichever stack bank is active. The API rejects invalid identifiers
and values before traffic; it does not check whether an address is mapped or
suitable for the program.

```go
err := core.WriteRegister(ctx, cortexm.R4, 42)
```

A write stages DCRDR, selects the register, and waits for transfer completion.
An error after attempting to stage data leaves only `Release` available. If
selection was attempted, the register may have changed even when the call
returns an error. Release settles a pending transfer without replaying it.
Successful writes are intentional changes to processor state: release does
not roll them back, and resumed execution uses the changed values. An inherited
halt permits writes but still does not grant permission to resume.

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

The [control example](../examples/simple/cortexm-control/main.go) selects one
probe and AP, halts, prints PC, SP, R0, and R4, resumes, then releases the
target before closing the
connection. It requires explicit consent to control execution:

```sh
go run ./examples/simple/cortexm-control \
  -provider cmsisdap -serial SERIAL -ap 0 -clock 1000000 -allow-control
```

Hardware-independent tests model DHCSR control and execution state, including
partial writes, canceled operations, ignored writes, failed cleanup, and
retry. They do not establish physical halt/resume behavior on a bench program.

## Hardware procedure

The opt-in integration test selects the CMSIS-DAP micro:bit with serial
`9900360140124e4500279015000000360000000097969901`, AP0, and a requested
1 MHz clock. The nRF51 needs at least 125 kHz during debug activation after
power-on; see the [startup evidence](protocols/cmsisdap.md#nrf51-startup-clock).
It requires a known firmware program with an aligned 32-bit RAM
counter incremented by the CPU at least once per 200 milliseconds. The counter
must not be updated by DMA or another processor. Loading firmware is outside
the test.

The [counter firmware](../target/cortexm/testdata/counter/README.md) supplies
a loop that increments the counter at `0x20000000`, with build instructions
and a separate programming procedure. Loading it replaces the target program
and resets the processor.

```sh
OSTIOLE_CORTEXM_HIL_CONTROL=1 \
OSTIOLE_CORTEXM_HIL_PROGRAM='program name and build identity' \
OSTIOLE_CORTEXM_HIL_COUNTER=0xRAM_ADDRESS \
go test -tags integration ./target/cortexm -run '^TestHILCortexM0Control$' -v
```

Two fresh sessions check counter progress before control, no progress during
a halt, and renewed progress after resume and after release from a second
halt. The test compares inherited debug-enable and halt status before closing
the Arm debug owner. It refuses an already-halted bench. These observations
do not establish peripheral behavior, register preservation, reset, stepping,
or restoration after a physical transport failure.

## Hardware evidence

On September 26, 2026, Nostalgia (macOS) completed two control sessions at
1 MHz on the selected micro:bit, with Cortex-M0 CPUID `0x410cc200`. The
counter image had been programmed and verified with OpenOCD 0.12.0. Its
Intel HEX SHA-256 was
`ee294cc06ab6e8228161b49506675b065c0148b26421cf1f83c8e45e35cd4e5d`.
After a physical replug, Ostiole's read-only test connected first at 1 MHz,
then the control test ran without any intervening OpenOCD session.

In both control sessions, the CPU counter advanced before acquisition,
remained unchanged across ten samples 20 milliseconds apart while halted,
and advanced after resume and after release from a second halt. The halted
values were `0x0d8abd3d` and `0x0db618ae`. DHCSR was `0x01000000` before
acquisition and after release in each session: debug was initially disabled,
acquisition enabled it, and release restored disabled debug with the processor
running. Both target releases and Arm debug owner closes completed.

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
`0x410cc200`. Each read R0–R12, SP, LR, PC, XPSR, MSP, and PSP while halted.
R4 accepted `0x55aa55aa` and `0xaa55aa55`; SP, MSP, PSP, and PC accepted
temporary aligned values. SP and MSP aliased as expected for this firmware.
The test restored each written value and compared all 19 registers with the
saved snapshot before resuming.

The CPU counter remained unchanged across ten samples 20 milliseconds apart
after register restoration, then advanced after resume and release. DHCSR was
`0x01000000` before acquisition and after release in both sessions, with debug
disabled and the processor running. Both target releases and Arm owner closes
completed. If register restoration cannot be confirmed, the test retains both owners
without requesting resume.

These sessions exercised register transfers while halted, not execution using
the temporary PC or stack values. Writes to the other general registers and LR,
process-stack selection, inherited halts, and failure cleanup have behavioral
test coverage only. XPSR writes, stepping, reset, and state after Arm owner
close were not tested.
