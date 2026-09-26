# Cortex-M control

`target/cortexm.Identify` reads CPUID through an aligned-word reader.
`Acquire` enables Cortex-M0 halting debug through borrowed `Memory` with
`ReadWord` and `WriteWord` methods, as supplied by `dap.MemAP`. It rejects
other processor parts before writing a debug register.

Acquisition does not request a halt. It preserves an inherited halt and
rejects active stepping, interrupt masking, and unfinished halt transitions.
When debug is disabled, other control bits are unknown; acquisition initializes
them to zero when enabling debug. Enabling debug can change how the processor
handles debug events. DHCSR reads consume its sticky reset and retirement
indicators, which cannot be restored.

Keep exclusive control of the debug registers and serialize all access to the
memory connection. Release the target before the memory owner. Each operation
is capped at five seconds or the caller's earlier deadline. Failed acquisition
attempts cleanup with an independent five-second context. A non-nil target
returned with an error retains cleanup obligations and permits only Release.
Failed restoration retains state for retry, including when a new observed halt
prevents restoring disabled debug. Cleanup requires usable memory; the target
cannot repair an invalidated MEM-AP or a poisoned transport.

```go
core, err := cortexm.Acquire(ctx, memory)
// Retain any non-nil core, including on error, for cleanup.
if err == nil {
    identity := core.Identity()
    _ = identity
}
cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
cleanupErr := core.Release(cleanupCtx)
cancel()
err = errors.Join(err, cleanupErr)
// Keep both owners on cleanup failure. Close the memory owner only after
// target release succeeds.
```

`Identity` returns the cached CPUID after release. A zero target cannot access
memory. This API does not request halt, resume, step, or reset.

Register semantics follow Arm DDI 0419E, sections C1.5 and C1.6.3 of the
[Armv6-M Architecture Reference Manual](https://documentation-service.arm.com/static/5f8ff05ef86e16515cdbf826).
Behavioral tests cover partial writes, cancellation, failed cleanup, and retry;
they do not establish physical execution-control behavior.
