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
