# Examples

Ostiole examples are grouped by how much of the library they compose.

`trivial/` contains small protocol demonstrations. They make one narrow
operation visible and are primarily useful for learning or hardware bring-up.

`simple/` contains focused inspection and control tools, such as processor
identity, CoreSight discovery, and Cortex-M0 halt/resume.

`advanced/` is reserved for composed workflows such as loading ELF payloads,
programming firmware or FPGA bitstreams, and extracting data through a target's
flash controller.

These categories describe the intended organization; they do not imply that an
example has been implemented.

## Available examples

- [`trivial/swd-dpidr`](trivial/swd-dpidr) reads the identification register of
  one SWD debug port through an explicitly selected FTDI attachment.
- [`simple/ap-id`](simple/ap-id) reports the debug-port identity and one
  explicitly selected access-port identity.
- [`simple/cortexm-info`](simple/cortexm-info) reads a Cortex-M processor
  identity through an explicitly selected memory access port.
- [`simple/arm-info`](simple/arm-info) discovers an opt-in probe provider and
  reads DPIDR, AP IDR, and Cortex-M identity through one Arm debug owner.
- [`simple/coresight-info`](simple/coresight-info) reads the advertised debug
  entry of a selected MEM-AP, or a known component page, through a managed SWD
  connection. Add `-walk` for bounded ROM traversal with partial-result
  reporting.
- [`simple/cortexm-control`](simple/cortexm-control) enables Cortex-M0 halting
  debug, halts and resumes the processor, then restores debug control. It
  preserves an already halted core without stepping or resuming it. It requires
  `-allow-control`; see [Cortex-M control](../docs/cortexm.md) for effects and
  cleanup limits.

For ADIv6 SW-DP targets, `coresight-info -debug-space -walk` inspects the DP's
advertised discovery tree. Use `-ap-base ADDRESS` instead of `-ap INDEX` to
inspect memory through one ADIv6 MEM-AP. The same probe selection and cleanup
rules apply. See the [RP2350 procedure][rp2350-procedure].

The `arm-info`, `coresight-info`, and `cortexm-control` examples accept `-clock`
in Hz and default to 1 MHz. FTDI and J-Link supply general SWD wires; CMSIS-DAP
supplies completed register operations. `armdebug` selects the advertised
register surface before activation, with no fallback after failure.

[rp2350-procedure]: ../docs/coresight.md#adiv6-and-the-rp2350

## Cancellation and cleanup

Interrupt and termination signals cancel each example's operation context.
Cleanup uses a fresh bounded context before the program exits, and its errors
are joined with any operation failure. The managed examples retain errors from
earlier cleanup attempts even when a later retry succeeds.

The direct FTDI examples release their SWD or DAP owner before closing the
channel. A failed release retains the channel while protocol restoration is
still pending. A completed SWD or DAP release can report a context error; host
cleanup then proceeds while preserving that error. Known terminal adapter loss
skips further protocol traffic and reports the loss while releasing host
resources. These small programs report remaining cleanup failures; exiting
cannot restore target state.
