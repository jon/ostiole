# Cortex-M0 counter firmware

This micro:bit v1 bench program increments the 32-bit word at `0x20000000`
in a CPU loop. It disables interrupts and uses no peripheral or DMA engine.
The vector table starts at flash address zero and uses the top of the first
16 KiB of RAM for the initial stack pointer. Every unexpected exception loops
without changing the counter.

Build from the repository root with Clang's Arm assembler, LLD, and GNU Arm
objcopy:

```sh
clang --target=arm-none-eabi -mcpu=cortex-m0 -mthumb -c \
  target/cortexm/testdata/counter/counter.S -o /tmp/ostiole-counter.o
ld.lld -T target/cortexm/testdata/counter/counter.ld \
  /tmp/ostiole-counter.o -o /tmp/ostiole-counter.elf
arm-none-eabi-objcopy -O ihex /tmp/ostiole-counter.elf /tmp/ostiole-counter.hex
```

Loading this image replaces the target program and resets the processor. It
does not update the DAPLink interface firmware. Select the exact probe when
using an external programmer. Programming is bench preparation, separate from
the Ostiole [control test](../../../../docs/cortexm.md#hardware-procedure).

For the selected micro:bit, use OpenOCD's CMSIS-DAP v2 transport and nRF51
flash driver at 1 MHz. The nRF51 requires at least 125 kHz when entering
debug interface mode after power-on; 100 kHz can work after another debugger
has already activated it. See the
[startup evidence](../../../../docs/protocols/cmsisdap.md#nrf51-startup-clock).

```sh
openocd -f interface/cmsis-dap.cfg \
  -c 'cmsis_dap_backend usb_bulk' \
  -c 'adapter serial 9900360140124e4500279015000000360000000097969901' \
  -f target/nrf51.cfg -c 'adapter speed 1000' \
  -c 'gdb_port disabled; tcl_port disabled; telnet_port disabled' \
  -c 'program /tmp/ostiole-counter.hex verify reset exit'
```

After successful verification and reset, run the control test with
`OSTIOLE_CORTEXM_HIL_COUNTER=0x20000000`. Use the image's SHA-256 as the program
identity in `OSTIOLE_CORTEXM_HIL_PROGRAM`.
