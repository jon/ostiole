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

Ostiole is at an early, exploratory stage. This revision is a deliberately
small proof of life: one pure-Go Linux program opens a generic FTDI H-series
attachment, speaks Serial Wire Debug through its MPSSE interface, and reads a
debug port's identification register.

The implementation is still contained in the program itself. Later revisions
will extract its working pieces into reusable packages without changing the
wire behavior established here.

USB attachment discovery and exact-device opening now live in `usb`;
the package also owns the selected interface and performs bounded control
and bulk transfers. The proof no longer performs Linux USB operations itself.

The proof explicitly binds the selected attachment to FTDI MPSSE port A
for SWD. The FTDI package also exchanges exact command and response
payloads, owns the interface, and restores it on close. Protocol framing and
MPSSE clock setup remain visible in `main.go`; command-stream synchronization
is handled by the channel.

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
Early hardware work is deliberately read-only. Operations with broader effects
should be explicit and separately gated.

## Linux proof of life

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
sudo go run .
```

A successful read prints only the debug-port identity, for example
`DPIDR=0x2ba01477`. The operation does not halt or reset the target and does
not write target memory.

## License

Ostiole is licensed under the [Apache License 2.0](LICENSE).
