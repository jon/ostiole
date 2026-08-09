---
applyTo: "**/*_integration_test.go"
---

# Hardware integration-test review rules

- Require the `integration` build tag and treat absent or ambiguous hardware
  as a skip before an adapter is selected.
- After selection, require exactly one open attempt; contention and every
  other open failure must fail the test.
- Keep ordinary identity tests read-only. Require explicit opt-in for reset,
  halt, target writes, or other effectful operations.
- Verify volatile target and adapter state is restored with bounded cleanup,
  including when the primary operation fails.
- Do not accept compilation, simulation, or an unexecuted integration test as
  HIL evidence. Physical claims must identify the exercised path and bench.
