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
native Linux and macOS USB access, an explicitly configured FTDI MPSSE path,
and conservative raw Serial Wire Debug transactions without automatic
retries.

The `swd/sim` package provides a hardware-free behavioral wire that models SWD
protocol entry and basic DP/AP register transfers against a caller-supplied
target.

The `dap` package begins the next layer with ADIv5 debug-port identity, raw
SW-DP registers, and an explicit connection lifecycle. `NewAPSel` constructs
an access-port selector whose zero value is invalid. A connection clears
sticky status, selects the base register bank, and restores only the power
requests it acquired. `DebugPort.Connect` performs SWD entry; later DP, AP,
transaction, and MEM-AP operations require that active connection.
`ReadAPIDR` reads and decodes AP identity. For raw access, `APSel.Address`
combines a selector with the complete eight-bit register address. Posted reads
and writes complete through `RDBUFF`. The simulator models the same DP and AP
state changes. Raw access has the effects defined by the selected AP class;
writing a MEM-AP data register can write target memory. Any raw access which
completes or might have completed invalidates existing `MemAP` values.
`dap.DebugPort` retries only the physical request that returned WAIT.
After an extended AP stall, it issues DAPABORT rather than replaying the whole
logical access. A MEM-AP client and its model can read arbitrary byte ranges and
perform aligned scalar reads and writes, then restore the CSW, TAR, and optional
TARHI values changed by that access. Cleanup terminates an incomplete 64-bit
transfer through CSW before restoring the target address registers. A block
read retries the same request after WAIT while selection and framing remain
known, WAIT cleanup succeeds, and its context remains active.

The [examples](examples) begin with a raw SWD debug-port identity read, then
add posted access-port reads and a Cortex-M identity read through a MEM-AP.
They compose the public packages explicitly without duplicating their framing.
The `target/cortexm` package reads and decodes the architectural CPUID value
through any compatible target-word reader.

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

## Documentation

The [documentation](docs) describes the current package architecture,
ownership rules, composition paths, capability boundaries, and safety effects.
It is written for people and tools building applications with Ostiole.

People and coding agents changing Ostiole itself should follow the shared
[contribution guide](CONTRIBUTING.md).

## Requirements

Ostiole requires Go 1.25.12 or newer. On macOS 12 or newer, install Xcode or
the Xcode command-line tools. The native USB implementation uses cgo to call
the system IOKit and CoreFoundation frameworks; it does not require libusb or
a third-party USB package.

On Linux, grant the interactive user access to the exact USB product and
release any kernel driver bound to the interface before running a hardware
command. Never work around device permissions by running repository code as
root. See [Linux USB access](docs/linux-usb.md) for udev rules and a bounded
`ftdi_sio` release and restoration procedure.

## Safety

Debug and programming interfaces can reset processors, halt execution, modify
memory, reconfigure programmable logic, and change persistent device state.
The shipped examples and `ost` commands avoid reset, halt, target-memory
writes, and persistent changes. The `dap.MemAP` API does expose effectful
scalar writes; callers choose the addresses and own the consequences.
Establishing an ADIv5 connection also changes volatile debug-port control
state; the connection releases its own power requests before return.

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
used. After configuring [Linux USB access](docs/linux-usb.md), when applicable,
run:

```sh
go run ./examples/trivial/swd-dpidr
```

This path is validated with an FT232H (`0403:6014`) on Linux and macOS. On
Linux, the current implementation requires an explicit driver release and
restoration. On macOS, claiming the device temporarily seizes its USB
interface from the Apple FTDI driver; closing it releases that ownership.

A successful read prints only the debug-port identity, for example
`DPIDR=0x2ba01477`. The operation does not halt or reset the target and does
not write target memory.

Maintainers can exercise the same public-library path as an opt-in hardware
test:

```sh
OSTIOLE_FTDI_HIL=1 go test -tags integration ./swd
```

On macOS, maintainers can validate FT232H interface ownership and the FTDI
control and bulk path without touching a downstream target:

```sh
OSTIOLE_FT232H_DARWIN_HIL=1 \
  go test -tags integration ./ftdi \
  -run '^TestHILDarwinFT232HMPSSEHandshake$' -v
```

This test selects exactly one FT232H (`0403:6014`), opens it once, and performs
only MPSSE setup and synchronization on port A at 400 kHz. It does not
construct an SWD connection, clock JTAG, reset a target, or access downstream
devices.

## Access-port identity example

The simple access-port example uses the same wiring and selects AP0 explicitly.
It connects through the DAP layer, reads the access-port identification
register through the posted-read pipeline, and releases the power requests it
acquired. Run:

```sh
go run ./examples/simple/ap-id
```

A successful read prints both identities, for example
`DPIDR=0x2ba01477 AP0_IDR=0x24770011`. The operation does not reset or halt the
target and does not access target memory.

Maintainers can exercise the DAP path as an opt-in hardware test:

```sh
OSTIOLE_FTDI_HIL=1 go test -tags integration ./dap
```

## Cortex-M identity example

The Cortex-M example selects AP0 explicitly, requires it to be a MEM-AP, and
reads the architectural CPUID register. Run:

```sh
go run ./examples/simple/cortexm-info
```

A successful read prints the debug port, access port, and processor identities,
for example `DPIDR=0x2ba01477 AP0_IDR=0x24770011 CPUID=0x410fc241`.
The operation restores CSW, TAR, DP selection, and acquired power state. It
does not reset or halt the target or write target memory.

## Command

`ost` is a small command-line companion built from the same public packages
used by the examples. It can list supported FTDI attachments without opening
them:

```sh
go run ./cmd/ost ftdi list
```

Each result reports the USB bus and address followed by its vendor and product
identifiers. A supported attachment can perform the same raw DPIDR read as the
trivial example:

```sh
go run ./cmd/ost swd dpidr
```

The DAP form establishes the explicit debug-port lifecycle and reports the
decoded identity fields before releasing the power state it acquired:

```sh
go run ./cmd/ost dap dp id
```

One access port can be selected explicitly through the same lifecycle:

```sh
go run ./cmd/ost dap ap id --ap 0
```

The first target-level command identifies a Cortex-M processor through one
explicitly selected MEM-AP:

```sh
go run ./cmd/ost target cortex-m id --ap 0
```

It reports DPIDR, the selected AP IDR, and CPUID while restoring MEM-AP and
debug-port state. None of these commands reset or halt the target, write target
memory, or change persistent state.

Run `go run ./cmd/ost help` for the available command hierarchy.

## License

Ostiole is licensed under the [Apache License 2.0](LICENSE).
