---
applyTo: "**/*.md"
---

# Documentation review rules

- Describe only behavior present in the reviewed commit; do not turn plans or
  likely future support into current capability claims.
- Keep architecture ownership, cleanup, safety effects, composition guidance,
  examples, commands, and capability tables consistent with code.
- Distinguish implemented behavior, behavioral simulation, CI compilation,
  and physical HIL. Do not infer one form of evidence from another.
- Require documentation changes in the same commit as changes to exported
  APIs, package responsibilities, lifecycle, safety, platforms, compositions,
  or validation claims.
- Verify relative links, headings, commands, package names, examples, and
  stated limitations against the current tree.
