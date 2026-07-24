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

Ostiole is at an early, exploratory stage. This revision contains repository
scaffolding only and does not yet communicate with hardware.

The first working slice is intended to be deliberately modest: pure-Go USB
access on Linux, a generic FTDI device with an MPSSE-capable interface, Serial
Wire Debug, and read-only identification of a generic Arm Cortex-M target.

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

## License

Ostiole is licensed under the [Apache License 2.0](LICENSE).
