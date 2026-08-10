# Capabilities

This guide describes the behavior implemented by the current public packages.
It distinguishes an API from the environments where that API is exercised in
CI or on physical hardware.

“Implemented” means the behavior is present and covered by ordinary tests.
“Simulated” means a public behavioral model exercises the same package
boundary. “HIL” means an opt-in integration test has exercised physical
hardware. None of these labels imply support for every device in a product
family or every feature of a protocol.

## Host USB

| Capability | Implemented | Validation and boundary |
| --- | --- | --- |
| Linux host access | Yes | Pure-Go sysfs inventory and usbfs transfers; Linux CI and FT232H HIL. Permission setup and manual release of a bound kernel driver are host prerequisites. |
| macOS host access | Yes | IOKit and IOUSBLib through cgo; macOS 26 arm64 and Intel CI with a macOS 12 deployment target. |
| Filtered enumeration | Yes | VID/PID filters, deterministic bus/address ordering, and context checks. |
| Exact open | Yes | Revalidates bus, address, vendor, and product before and after opening. |
| Interface ownership | Yes | One claimed interface, alternate selection, release, and close. Linux reports contention rather than detaching a bound kernel driver. |
| Control transfers | Yes | Synchronous, deadline-bounded endpoint-zero transfers. |
| Bulk transfers | Yes | Synchronous, deadline-bounded bulk IN and OUT transfers. |
| Linux FT232H ownership | HIL | Manual `ftdi_sio` unbind, unprivileged usbfs claim and MPSSE/SWD traffic, release, and explicit driver rebind. |
| macOS FT232H ownership | HIL | Interface seizure, control/bulk traffic, MPSSE setup, close, and Apple driver rematch. |

The USB package does not currently expose descriptor trees, device strings,
hotplug events, multiple simultaneous interface claims, interrupt or
isochronous transfers, device reset, or configuration switching.

Linux is the only pure-Go host. macOS builds require cgo and the Xcode or
Xcode command-line-tool SDK; they do not require libusb or another installed
USB library. Windows is not supported. See [Linux USB access](linux-usb.md)
for the host setup required by physical Linux USB operations.

## FTDI MPSSE

| Capability | Implemented | Validation and boundary |
| --- | --- | --- |
| FT232H | Yes | Port A; full MPSSE and SWD HIL on Linux and macOS. |
| FT2232H | Yes | Ports A and B using the standard H-series interface and endpoint layout. |
| FT4232H | Yes | Ports A and B using the standard H-series interface and endpoint layout. |
| Explicit clock | Yes | Divisor selects a rate no faster than requested; examples use 400 kHz. |
| MPSSE lifecycle | Yes | Claim, reset, synchronize, configure pins/clock, reset bit mode, set the latency timer to 16 ms, purge the receive and transmit paths, release, and close. |
| SWD bit streams | Yes | Direction-safe output and input runs with exact bulk exchange. |
| JTAG | No | No public JTAG engine or FTDI JTAG interface exists. |

The driver binds the standard FTDI H-series interfaces and endpoint numbers.
It does not inspect USB descriptors to verify a different layout. A listed
USB identity is a candidate, not evidence that every board using that identity
wires its MPSSE port for debugging.

## Serial Wire Debug

| Capability | Implemented | Validation and boundary |
| --- | --- | --- |
| Line reset | Yes | Unit tested. |
| JTAG-to-SWD selection | Yes | Unit tested and used by every SWD HIL path. |
| DP and AP requests | Yes | One request at a time with header, turnaround, data, and idle cycles. |
| ACK classification | Yes | OK, WAIT, FAULT, and invalid acknowledgements are distinguished. |
| Read parity | Yes | Invalid read parity is reported. |
| Automatic retries | No | WAIT and FAULT return directly to the caller. |
| Batching or pipelining | No | Each call executes one complete transaction. |
| Multidrop or dormant state | No | The public connection models one entered SWD target. |
| Behavioral simulation | Yes | Protocol entry and basic DP/AP register transfers. |
| Physical DPIDR read | HIL | Opt-in FTDI test and trivial example on Linux and macOS. |

The public `swd.Wire` boundary permits another wire implementation, but FTDI
is the only physical implementation currently provided.
The [Serial Wire Debug guide](protocols/swd.md) gives the bit-level protocol,
specification notes, and current physical observation.

## Debug Access Port and MEM-AP

| Capability | Implemented | Validation and boundary |
| --- | --- | --- |
| DPIDR decoding | Yes | Validates the constant bit and exposes raw identity fields. |
| SW-DP connection | Yes | Clears sticky state, selects bank zero, requests acknowledged power, and records ownership. |
| SW-DP release | Yes | Clears only power requests acquired by the connection; failed release can be retried. |
| Raw DP access | Yes | Reads and writes the currently addressed DP registers over SWD. |
| Selected AP access | Yes | Explicit `APSel`, bank selection, posted reads through RDBUFF, and completed writes. |
| AP enumeration | No | Callers must select an access port explicitly. |
| MEM-AP validation | Yes | Rejects an absent AP or an AP whose IDR is not a MEM-AP. |
| Target word read | Yes | One aligned 32-bit word with address increment disabled. |
| MEM-AP restoration | Yes | Saves and restores CSW and TAR; failed restoration remains retryable. |
| Target-memory writes | No | No public MEM-AP write operation exists. |
| Block or sub-word access | No | No burst, auto-increment, 8-bit, or 16-bit operation exists. |
| ADIv6 or JTAG-DP | No | The public implementation is the current minimal ADIv5 SW-DP path. |
| Behavioral simulation | Yes | DP identity/power, posted AP access, and configured target words. |
| AP and MEM-AP reads | HIL | Opt-in FTDI integration tests against an explicitly selected AP. |

Connecting and reading a MEM-AP changes volatile debug state even though it
does not write target memory. Applications must release the MEM-AP before the
debug port so CSW and TAR are restored, bank selection returns to zero, and
acquired power is released.
The [Arm Debug Access Port guide](ports/dap.md) describes ADIv5 register
access, posted transactions, power handshakes, and the current bench result.

## Cortex-M target operations

| Capability | Implemented | Validation and boundary |
| --- | --- | --- |
| CPUID read and decode | Yes | Accepts any aligned-word reader and validates a plausible Arm Cortex-M identity. |
| Physical identity read | HIL | Opt-in FTDI/SWD/DAP/MEM-AP integration test. |
| Halt, resume, or step | No | No target run-control API exists. |
| Register access | No | CPUID decoding is not a general core-register interface. |
| Reset | No | No architectural or pin-reset operation exists. |
| Breakpoints or watchpoints | No | No target instrumentation API exists. |
| Firmware or runtime loading | No | No ELF loader, flash driver, or target-memory writer exists. |

The package identifies a processor; it is not yet a complete Cortex-M target
driver.

## Executable surfaces

Available examples:

- `examples/trivial/swd-dpidr` reads one raw DPIDR.
- `examples/simple/ap-id` reports DPIDR and one explicitly selected AP IDR.
- `examples/simple/cortexm-info` reports DPIDR, AP IDR, and Cortex-M CPUID.

Available `ost` commands:

```text
ost ftdi list
ost swd dpidr
ost dap dp id
ost dap ap id --ap N
ost target cortex-m id --ap N
```

These hardware operations are read-only with respect to target memory and do
not halt or reset the target. They still claim the adapter, clock SWD, and use
the volatile DAP and MEM-AP state described above.

## Not currently provided

There is no public J-Link or CMSIS-DAP driver, JTAG protocol layer, automatic
probe discovery policy, AP discovery, CoreSight or ROM-table discovery,
multi-core or SoC attachment, general target control, semihosting, trace,
debugger protocol server, firmware flashing, FPGA programming, or Windows
host implementation.

Treat an absent capability as an explicit boundary. Do not infer it from the
project description or recreate its lower-level protocol inside an
application. See [Composing Ostiole](composition.md) for selecting and
extending the current layers.
