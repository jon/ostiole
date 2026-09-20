# Examples

Ostiole examples are grouped by how much of the library they compose.

`trivial/` contains small protocol demonstrations. They make one narrow
operation visible and are primarily useful for learning or hardware bring-up.

`simple/` is reserved for focused inspection tools that could be useful on
their own, such as processor, CoreSight, or ROM-table discovery.

`advanced/` is reserved for composed workflows such as loading ELF payloads,
programming firmware or FPGA bitstreams, and extracting data through a
target's flash controller.

These categories describe the intended organization; they do not imply that
an example has been implemented.

## Available examples

- [`trivial/swd-dpidr`](trivial/swd-dpidr) reads the identification register
  of one SWD debug port through an explicitly selected FTDI attachment.
- [`simple/ap-id`](simple/ap-id) reports the debug-port identity and one
  explicitly selected access-port identity.
- [`simple/cortexm-info`](simple/cortexm-info) reads a Cortex-M processor
  identity through an explicitly selected memory access port.
- [`simple/arm-info`](simple/arm-info) discovers an opt-in probe provider and
  reads DPIDR, AP IDR, and Cortex-M identity through one Arm debug owner.
- [`simple/coresight-info`](simple/coresight-info) reads the advertised debug entry
  of a selected MEM-AP, or a known component page, through a managed SWD connection.
  Add `-walk` for bounded ROM traversal with partial-result reporting.

For ADIv6 SW-DP targets, `coresight-info -debug-space -walk` inspects the DP's
advertised discovery tree. Use `-ap-base ADDRESS` instead of `-ap INDEX` to
inspect memory through one ADIv6 MEM-AP. The same probe selection and cleanup
rules apply. See the [RP2350 procedure](../docs/coresight.md#adiv6-and-the-rp2350).
