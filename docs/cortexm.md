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

The implementation follows Arm DDI 0419E, sections C1.5 and C1.6.3 of the
[Armv6-M Architecture Reference Manual](https://documentation-service.arm.com/static/5f8ff05ef86e16515cdbf826).
It does not implement reset, single-step, general register access, breakpoints,
or watchpoints.

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
probe and AP, halts, resumes, then releases the target before closing the
connection. It requires explicit consent to control execution:

```sh
go run ./examples/simple/cortexm-control \
  -provider cmsisdap -serial SERIAL -ap 0 -allow-control
```

Hardware-independent tests model DHCSR control and execution state, including
partial writes, canceled operations, ignored writes, failed cleanup, and
retry. They do not establish physical halt/resume behavior on a bench program.

## Hardware procedure

The opt-in integration test selects the CMSIS-DAP micro:bit with serial
`9900360140124e4500279015000000360000000097969901`, AP0, and a requested
100 kHz clock. It requires a known firmware program with an aligned 32-bit RAM
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

On September 26, 2026, Nostalgia (macOS) completed the control test in two
fresh sessions on the selected micro:bit, with Cortex-M0 CPUID `0x410cc200`.
OpenOCD 0.12.0 programmed and verified the counter image using the procedure
above. The Intel HEX image's SHA-256 was
`ee294cc06ab6e8228161b49506675b065c0148b26421cf1f83c8e45e35cd4e5d`.

In both sessions, the CPU counter advanced before acquisition, remained
unchanged across ten samples 20 milliseconds apart while halted, and advanced
after resume and after release from a second halt. The halted values were
`0x014c4757` and `0x017ebe6c`. Both target releases and Arm debug owner closes
completed. DHCSR showed debug enabled and the processor running before
acquisition and after release in each session; the first read also consumed
the sticky reset indicator.

This run covers inherited enabled debug on one micro:bit. It does not verify
enabling and restoring initially disabled debug, restoration after closing
the Arm debug owner, or cleanup after a physical transport failure. Earlier
attempts with Ostiole and OpenOCD could not read DPIDR; the cause of that
connection failure and its recovery remain unknown.
