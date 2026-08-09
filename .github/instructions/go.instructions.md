---
applyTo: "**/*.go"
---

# Go review rules

- Require idiomatic, naturally formatted Go beyond mechanical `gofmt`
  acceptance. Prefer straightforward control flow and conventional local and
  receiver names.
- Check every returned error and preserve the primary operation error when
  cleanup also fails. Cleanup that may be retried must retain enough state to
  retry safely.
- Verify context deadlines and cancellation cover blocking host and protocol
  operations without preventing bounded cleanup.
- Check slice bounds, integer widths, byte order, parity, bit fields, and
  protocol state transitions at trust boundaries.
- Report data races, unsafe shared state, goroutine leaks, and ownership that
  is unclear across constructors, open calls, release methods, and `Close`.
- Keep commands thin and reusable behavior in public library packages. Keep
  production packages independent of simulators and command-internal policy.
- Require focused tests for success, failure, cleanup, and retry behavior at
  the package boundary that owns the change.
- Apply the production complexity limits in `.golangci.yml` and the separate
  test limits in `.golangci.tests.yml`; do not encourage formatting tricks to
  affect those measurements.
