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

Hardware-independent tests model DHCSR control and execution state, including
partial writes, canceled operations, ignored writes, failed cleanup, and
retry. They do not establish physical halt/resume behavior on a bench program.
