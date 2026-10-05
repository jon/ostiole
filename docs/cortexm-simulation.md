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
`OpenMemAP` and `cortexm.Identify`. Close the shared Arm owner after memory
consumers finish; retain dependencies when cleanup is pending. Serialize traffic
and all fixture operations.

The model supplies only register observations. All writes and unknown register
addresses bus-fault, as do non-word accesses. DHCSR reads consume sticky reset,
retirement and M33 restart status; snapshots do not. New cores validate their
initial profile, reserved bits and supported inherited Debug state. Constructor
and snapshots perform no target traffic.

Tests identify two M0 or Secure M33 cores through shared Arm/SWD/DAP owners,
including RP2350-shaped ADIv6 APs, then close the owner without changing CPU
debug state. These tests do not establish physical debug behavior. Control
writes, event scheduling, register transfers, instruction execution, alternate
security profiles, CTI and USB/probe emulation are outside this read-only model.

The modeled rules follow Arm DDI 0419E C1.5/C1.6.2–3 and DDI 0553B.y
D1.2.38/D1.2.39; see [Cortex-M control](cortexm.md) for the architecture
references.
