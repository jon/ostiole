# Ostiole

Ostiole is an experimental Go library for building self-contained tools that
communicate with, configure, program, and inspect embedded hardware.

It provides small, composable packages spanning the path from host transports
and hardware adapters through wire protocols and device-specific operations.
Applications can use only the layers they need and explicitly assemble them.

Ostiole is a library rather than a packaged debugger. A program can embed
firmware, FPGA bitstreams, runtime code, or other payloads and communicate with
supported hardware directly, without requiring users to install additional
tools or libraries.

An ostiole is a small opening or pore. The name reflects the library's intended
role as a narrow, controlled way to observe and interact with another system.

## Motivation

Many development boards expose programming or debugging facilities through
USB, whether through an external probe or an adapter built into the board.
Using those facilities often requires users to install a separate suite of
tools, locate the correct adapter configuration, and reproduce a particular
command or script.

Ostiole is intended to make another style of tool possible: a Go program that
contains the operation, its hardware support, and any payload it needs. Such a
program might flash firmware, configure an FPGA, load code into a running
target, inspect a system, or provide a project-specific recovery utility.

## Status

Ostiole is at an early, exploratory stage. The available packages provide
pure-Go Linux USB access, an explicitly configured FTDI MPSSE path, and
conservative Serial Wire Debug transactions without automatic retries.

The `swd/sim` package provides a hardware-free behavioral wire that models SWD
protocol entry and basic DP/AP register transfers against a caller-supplied
target.

The `dap` package begins the next layer with ADIv5 debug-port identity, raw
SW-DP registers, and an explicit connection lifecycle. A connection clears
sticky status, selects the base register bank, and restores only the power
requests it acquired. Selected AP registers are read and written through their
posted `RDBUFF` completion path. The simulator models the same DP and AP state
changes. A minimal MEM-AP client and its model can read one aligned 32-bit
target word without address incrementing, then restore the CSW and TAR values
changed by that read.

The [examples](examples) begin with a raw SWD debug-port identity read, then
add posted access-port reads and a Cortex-M identity read through a MEM-AP.
They compose the public packages explicitly without duplicating their framing.

This initial FTDI path uses the standard H-series MPSSE port and endpoint
layout. Descriptor-driven port binding is not implemented yet.

## Design direction

Ostiole keeps the major hardware-access layers separate:

```text
host transport → adapter driver → wire protocol → device access → operation
```

Low-level packages expose mechanisms rather than guessing policy. Hardware is
selected explicitly, protocol state belongs to one logical owner, and
higher-level code should not duplicate framing implemented by lower layers.

The initial implementation favors a small, understandable path over broad
device support. Additional transports, adapters, protocols, and targets can be
introduced as independent pieces once their behavior is specified and tested.

## Safety

Debug and programming interfaces can reset processors, halt execution, modify
memory, reconfigure programmable logic, and change persistent device state.
Early hardware work avoids reset, halt, target-memory writes, and persistent
changes. Establishing an ADIv5 connection does change volatile debug-port
control state; the connection releases its own power requests before return.

## SWD DPIDR example

The program expects exactly one supported FTDI H-series attachment and uses
MPSSE port A at 400 kHz. Connect it to a powered SWD target as follows:

| Adapter signal | Target signal |
| --- | --- |
| D0 | SWCLK |
| D1 through a 1 kΩ series resistor | SWDIO |
| D2 | SWDIO |
| GND | GND |

The target supplies its own power. No reset or target-power connection is
used. On Linux, run:

```sh
sudo go run ./examples/trivial/swd-dpidr
```

A successful read prints only the debug-port identity, for example
`DPIDR=0x2ba01477`. The operation does not halt or reset the target and does
not write target memory.

Maintainers can exercise the same public-library path as an opt-in hardware
test:

```sh
sudo OSTIOLE_FTDI_HIL=1 go test -tags integration ./swd
```

## Access-port identity example

The simple access-port example uses the same wiring and selects AP0 explicitly.
It connects through the DAP layer, reads the access-port identification
register through the posted-read pipeline, and releases the power requests it
acquired. Run:

```sh
sudo go run ./examples/simple/ap-id
```

A successful read prints both identities, for example
`DPIDR=0x2ba01477 AP0_IDR=0x24770011`. The operation does not reset or halt the
target and does not access target memory.

Maintainers can exercise the DAP path as an opt-in hardware test:

```sh
sudo OSTIOLE_FTDI_HIL=1 go test -tags integration ./dap
```

## Cortex-M identity example

The Cortex-M example selects AP0 explicitly, requires it to be a MEM-AP, and
reads the architectural CPUID register. Run:

```sh
sudo go run ./examples/simple/cortexm-info
```

A successful read prints the debug port, access port, and processor identities,
for example `DPIDR=0x2ba01477 AP0_IDR=0x24770011 CPUID=0x410fc241`.
The operation restores CSW, TAR, DP selection, and acquired power state. It
does not reset or halt the target or write target memory.

## License

Ostiole is licensed under the [Apache License 2.0](LICENSE).
