# Documentation

Ostiole is a collection of composable Go packages rather than a complete
debugger application. These guides explain the packages that are available
today and how to assemble them without duplicating lower-level behavior.

- [Architecture](architecture.md) describes package responsibilities,
  ownership, cleanup, and safety effects.
- [Composition](composition.md) maps common tasks to the narrowest public
  package that implements them and gives coding agents a selection checklist.
- [Examples](../examples) contains executable compositions that progress from
  direct protocol demonstrations to focused inspection tools.

The documentation describes the current tree. It does not promise future
probe, protocol, target, or application support.
