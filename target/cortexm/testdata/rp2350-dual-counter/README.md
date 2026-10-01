# RP2350 independent RAM counters

Two Cortex-M33 loops increment separate words in shared SRAM. Core 0 uses
`0x20040000` with code at `0x20040020`; core 1 uses `0x20040004` with code at
`0x20040060`. Each loop adds one to R0, stores through R1, and branches back.
Neither loop uses a stack, peripherals, or DMA. Both run in Secure Thread mode
with configurable interrupts disabled.

Build from the repository root:

```sh
clang --target=arm-none-eabi -mcpu=cortex-m33 -mthumb -c \
  target/cortexm/testdata/rp2350-dual-counter/counter.S \
  -o /tmp/ostiole-rp2350-dual-counter.o
ld.lld -T target/cortexm/testdata/rp2350-dual-counter/counter.ld \
  /tmp/ostiole-rp2350-dual-counter.o -o /tmp/ostiole-rp2350-dual-counter.elf
arm-none-eabi-objcopy -O binary /tmp/ostiole-rp2350-dual-counter.elf \
  /tmp/ostiole-rp2350-dual-counter.bin
shasum -a 256 /tmp/ostiole-rp2350-dual-counter.bin
```

The binary SHA-256 is
`bf878b47815bc5eaf6afb5279efaa6ff163832bd178c5b7b1604f80e6ad6cde9`.

## Preparation

Stop other debuggers. The preparation script selects J-Link EDU Mini V2
`000802011345` at 1 MHz and creates independent OpenOCD targets at ADIv6 AP
`0x2000` and `0x4000`. It requires both inherited CTIs to be disabled and
unrouted, with no pending output or input channel, integration mode disabled,
and the default channel gate `0x0f`. It rejects core 1 held in inherited reset.
Secure execution, Secure debugger register-bank selection, and Secure invasive
debug permission are required before preparation and again immediately before
the core-1 MPU write. A conflicting override is rejected without changing DSCSR.
No CTI configuration or acknowledgement is written.

```sh
openocd -f target/cortexm/testdata/rp2350-dual-counter/prepare.cfg
```

Preparation resets core 1 through PSM FRCE_OFF.PROC1, using the atomic set/clear
aliases and preserving other reset bits. It then halts both cores, writes
`0x20040000`–`0x2004006f`, and compares all 28 words through both MEM-APs. This
verification reads memory on the host; it runs no checksum algorithm on either
processor. Core 1's boot-ROM MPU configuration prevents direct entry into this
RAM loop, so preparation disables its Secure MPU and sets its Secure MSP to
`0x20043000`. Both PCs, XPSRs, and interrupt masks are set before resume, then
halting debug is disabled. The loops overwrite R0/R1 and counter values.

Flash, OTP, core-0 reset, and CTI routing are untouched. The previous programs,
RAM contents, core-1 reset effects and MPU control, PCs, XPSRs, registers, stack
pointer, and interrupt masks are not restored. Use a bench where replacing both
cores' volatile execution state is acceptable, with Secure invasive debug
permitted and no other processor or DMA writer using this SRAM. This prepares a
dedicated bench; Ostiole's HIL test does not perform these setup effects.

## Validation

Run the [independent-core test][dual-bench] after preparation. It requires the
program identity and explicit effect gate, verifies counter code and inactive
CTIs before acquiring target control, and opens one shared Arm debug owner per
session. Calls remain serialized. Each core can halt and step while its peer
advances. Both targets are released before the memory owner closes.

The test restores inherited halting debug state. It leaves both RAM loops
running and does not undo their instruction effects or bench preparation.
Sequential stopping does not establish simultaneous halt or an atomic memory
snapshot. CTI propagation and acknowledgement require separate ownership.

[dual-bench]: ../../../../docs/cortexm.md#rp2350-independent-core-bench

Preparation guards can also be tested without a probe:

```sh
tclsh target/cortexm/testdata/rp2350-dual-counter/prepare_test.tcl
```

The mocked OpenOCD commands exercise the actual script, including a Non-secure
bank override inherited before preparation or observed after core-1 reset,
permission loss, Non-secure execution, and inherited CTI routing. The Go test
runs this check when `tclsh` is installed; absence skips that fixture check.
