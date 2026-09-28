# RP2350 core-0 RAM counter

This Cortex-M33 program disables configurable interrupts and increments the
32-bit word at `0x20040000` in a CPU loop. The code starts at `0x20040020` and
uses no stack, peripherals, or DMA. It runs in the core's existing Secure state.

Build from the repository root with Clang's Arm assembler, LLD, and GNU Arm
objcopy:

```sh
clang --target=arm-none-eabi -mcpu=cortex-m33 -mthumb -c \
  target/cortexm/testdata/rp2350-counter/counter.S -o /tmp/ostiole-rp2350-counter.o
ld.lld -T target/cortexm/testdata/rp2350-counter/counter.ld \
  /tmp/ostiole-rp2350-counter.o -o /tmp/ostiole-rp2350-counter.elf
arm-none-eabi-objcopy -O binary /tmp/ostiole-rp2350-counter.elf \
  /tmp/ostiole-rp2350-counter.bin
shasum -a 256 /tmp/ostiole-rp2350-counter.bin
```

The preparation script selects J-Link `000802011345` at 1 MHz and core 0's ADIv6
MEM-AP at `0x2000`. It halts core 0, loads and verifies the ELF, sets PC and
XPSR, resumes the counter, and disables halting debug for the control test.
OpenOCD uses `0x20041000`–`0x20041fff` as a backed-up working area.

```sh
openocd -f target/cortexm/testdata/rp2350-counter/prepare.cfg
```

This replaces core 0's running program state and writes the indicated RAM. Flash
is untouched, but the old PC, register values, interrupt-mask state, and
replaced RAM contents are not restored. No reset or core-1 halt is requested.
Use only on a bench where these changes are acceptable, with Secure invasive
debug allowed and no other processor or DMA writer using this RAM. Stop other
debuggers before running Ostiole.

The [RP2350 control test](../../../../docs/cortexm.md#rp2350-hardware-procedure)
observes the counter before acquisition, while halted, after resume, and after
release from another halt. Programming and verification are separate bench
preparation; the test does not load code or change core registers.
