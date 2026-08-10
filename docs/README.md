# Documentation

Ostiole is a collection of composable Go packages rather than a complete
debugger application. These guides explain the packages that are available
today and how to assemble them without duplicating lower-level behavior.

- [Architecture](architecture.md) describes package responsibilities,
  ownership, cleanup, and safety effects.
- [Serial Wire Debug](protocols/swd.md) describes the wire transaction, the
  parts of the specification which are easy to misread, and the current bench
  result.
- [Arm Debug Access Ports](ports/dap.md) describes the ADIv5 register window,
  posted AP access, power handshakes, and MEM-AP details worth testing.
- [Composition](composition.md) maps common tasks to the narrowest public
  package that implements them and gives coding agents a selection checklist.
- [Capabilities](capabilities.md) distinguishes implemented behavior from
  simulated, CI-tested, and HIL-validated configurations and explicit
  limitations.
- [Linux USB access](linux-usb.md) explains unprivileged device permissions
  and bounded release and restoration of a bound FTDI kernel interface.
- [Examples](../examples) contains executable compositions that progress from
  direct protocol demonstrations to focused inspection tools.

The documentation describes the current tree. It does not promise future
probe, protocol, target, or application support.
