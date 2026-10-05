# Cortex-M debug simulation

`target/cortexm/sim` supplies core-local debug registers beneath the real SWD,
DAP, MEM-AP and Cortex-M drivers. It models Cortex-M0 and Cortex-M33 Secure
execution with privileged Secure debugger access. The identities are
`0x410cc200` and `0x411fd210`. The simulator keeps the debug register state;
`cortexm.Target` and `cortexm.Group` own halt claims and restoration.

## Compose a core

Add a MEM-AP to a `dap/sim.Target`, then map the core's implemented word
registers before connecting the wire. Here `coresim` is `target/cortexm/sim` and
`dapsim` is `dap/sim`:

```go
clock := new(coresim.Clock)
core, err := coresim.New(coresim.Config{
    Profile: coresim.M33,
    Initial: coresim.Snapshot{DHCSR: 1 << 20}, // Secure invasive debug allowed.
    Clock: clock,
    HaltDelay: 10,
    ResumeDelay: 8,
})
if err != nil {
    return err
}
for _, addr := range []uint64{0xe000ed00, 0xe000ed30, 0xe000edf0} {
    if err := target.MapMEMAPDevice(sel, addr, 4, core); err != nil {
        return err
    }
}
before := core.Snapshot()
```

`sel` selects the MEM-AP already added to `target`. Map a different core through
each AP for distinct debug windows at the same addresses. Shared RAM may use a
single `dapsim.RAM` store through both APs. The core accepts only aligned,
little-endian word accesses; use little-endian MEM-AP fixtures for Cortex-M.
Unknown registers and other access widths bus-fault.

Compose `swd/sim` and a minimal probe backend using its public wire interface,
then use `armdebug.Connect`, `OpenMemAP`, and `cortexm.Acquire` or
`cortexm.AcquireGroup`. No simulated USB or adapter command framing is needed.
Release the target or group before closing the shared Arm owner. Retain those
owners when cleanup is pending. Serialize wire traffic, core access, snapshots,
and clock advancement.

## Requests, events and observations

A keyed DHCSR write changes the debug control request. Halt and resume complete
after their configured virtual delay. Zero delay completes during the accepted
write, including other events already due at that tick; `NoCompletion` leaves
the transition pending. Repeating an unchanged request does not postpone it.
Reads never advance time or complete a transition. DHCSR reads consume sticky
reset, retirement and M33 restart status; `Snapshot` consumes none of them. DFSR
writes clear the selected reason bits.

Advance the shared clock from the fixture's wire wrapper or scenario runner,
independently of which register the driver reads:

```go
if err := core.Schedule(clock.Now()+20, coresim.ExternalRestart); err != nil {
    return err
}
if err := clock.Advance(20); err != nil {
    return err
}
after := core.Snapshot()
```

Ticks have no physical duration. Host contexts still cancel actual driver calls.
The clock executes events in time order and preserves insertion order at equal
ticks. Warm reset invalidates older automatic completions. External restart does
so when leaving Debug state; requests received while running are ignored,
including while a halt is pending. An external halt does so when entering Debug
state; requests received while already in Debug state are ignored, including
while a resume is pending. External entry sets C_HALT and DFSR.EXTERNAL. Time
overflow and past scheduling fail before effects. Each core can share one clock
or use the private clock returned by `core.Clock()`.

Events can record retirement, warm reset, external restart or halt, and changes
to M33 Secure debug permission. Permission changes update the underlying
permission immediately; reported S_SDE remains latched while halted and updates
when the core leaves Debug state. Secure halt requests are ignored when debug is
denied. Warm reset clears the halt request, Debug state, interrupt mask and
retirement/restart indicators, sets reset status, and preserves debug enable,
permission, snap-stall control and DFSR. UNKNOWN reset fields use the documented
fixture values rather than an arbitrary physical implementation's values.

## Scope and evidence

The implemented registers are CPUID, DHCSR and DFSR. Stepping, register
transfers, instruction effects, other security profiles, sleeping, lockup,
interrupts, CTI, full reset/boot behavior, and USB/probe emulation are outside
this model. Initial snap-stall state can exercise acquisition refusal; setting
snap-stall or launching a step returns `ErrUnsupported`. Clearing initial
snap-stall control does not recover the memory state or permit resume.
Architecturally unpredictable interrupt-mask changes and disabling debug while
halted also return that model error before effects. Through `dap/sim`, callback
model errors become `ErrDeviceFailure`, preserving their diagnostic text rather
than posing as a bus acknowledgement or replaying an accepted access.

Behavioral tests compose two M0 or Secure M33 cores through one Arm owner, SWD
connection and DAP, including RP2350-shaped ADIv6 APs at `0x2000` and `0x4000`.
They cover inherited stops, selected resume, delayed completion, cancellation
with a fresh cleanup context, and retained cleanup after shared transport loss.
These tests do not establish physical halt/resume behavior.

The modeled control and observation rules follow Arm DDI 0419E C1.5/C1.6.2–3 and
DDI 0553B.y D1.2.38/D1.2.39. See the architecture references in
[Cortex-M control](cortexm.md).
