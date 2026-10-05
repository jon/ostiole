# Cortex-M debug simulation

`target/cortexm/sim` supplies Cortex-M0 and Secure Cortex-M33 CPUID, DHCSR and
DFSR beneath real SWD/DAP/MEM-AP drivers. The identities are `0x410cc200` and
`0x411fd210`; M33 uses Secure execution and privileged Secure DAP access. The
simulator keeps debug register state while `cortexm.Target` and `cortexm.Group`
own halt claims and restoration.

## Compose a core

Here `coresim` is `target/cortexm/sim`. Add a little-endian MEM-AP to a
`dap/sim.Target`, then map the implemented aligned-word registers before wire
traffic:

```go
core, err := coresim.New(coresim.Config{
    Profile: coresim.M33,
    Initial: coresim.Snapshot{DHCSR: 1 << 20},
})
if err != nil {
    return err
}
for _, addr := range []uint64{0xe000ed00, 0xe000ed30, 0xe000edf0} {
    if err := target.MapMEMAPDevice(sel, addr, 4, core); err != nil {
        return err
    }
}
snapshot := core.Snapshot()
```

Use separate cores through separate APs for private debug windows. Compose the
existing `swd/sim` wire and public probe interface, then use `armdebug.Connect`,
`OpenMemAP` and `cortexm.AcquireGroup`. Release the group before closing the
shared Arm owner; retain dependencies when cleanup is pending. Serialize traffic
and all fixture operations.

Keyed DHCSR writes enable debug and request halt/resume; completion uses
configurable virtual delays, defaulting to zero. Unkeyed writes are ignored.
DHCSR reads consume sticky reset, retirement and M33 restart status; snapshots
do not. DFSR is write-one-to-clear. Secure halt requests are ignored when
reported S_SDE is zero. Unsupported profiles, stepping and setting snap-stall
fail explicitly. Initial snap-stall can exercise acquisition refusal; clearing
its control bit does not recover memory or permit resume. Unknown registers and
non-word accesses bus-fault; unpredictable interrupt-mask writes and disabling
debug while halted return `ErrUnsupported` before effects. Through `dap/sim`,
model errors become `ErrDeviceFailure` without protocol classifications or
replay.

Tests run two M0 or Secure M33 cores through shared Arm/SWD/DAP owners,
including RP2350-shaped ADIv6 APs. They cover inherited halts, selected resume,
restoration and retained cleanup after shared transport loss. These tests do not
establish physical halt/resume behavior. Register transfers, instruction
execution, alternate security profiles, interrupts, full reset/boot behavior,
CTI and USB/probe emulation are outside this model.

The modeled rules follow Arm DDI 0419E C1.5/C1.6.2–3 and DDI 0553B.y
D1.2.38/D1.2.39; see [Cortex-M control](cortexm.md) for the architecture
references.

## Explicit virtual time

Share one `Clock` for multiple cores. Ticks have no physical duration; host
contexts still govern actual driver calls. Reads do not advance the clock or
complete a transition. Zero delay completes during an accepted write,
`NoCompletion` never completes, and repeating an unchanged request does not
postpone it. Overflow fails before accepting a control change.

```go
clock := new(coresim.Clock)
core, err := coresim.New(coresim.Config{
    Profile: coresim.M0,
    Clock: clock,
    HaltDelay: 10,
    ResumeDelay: 8,
})
if err != nil {
    return err
}
if err := clock.Advance(10); err != nil {
    return err
}
snapshot := core.Snapshot()
```

Advance from a fixture wire wrapper or scenario runner, independently of the
register being read. A nil configured clock creates a private clock available
through `core.Clock()`. Serialize advancement with core and wire access.
Completion runs in time order with stable insertion order for equal ticks.
Changed control invalidates older completions. Tests cover delayed and
never-completing requests, clock overflow, and deterministic cancellation after
a completed MEM-AP read followed by restoration with a fresh context.
