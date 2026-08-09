# FTDI adapter guidance

This file supplements the repository-wide agent and code-review rules for the
FTDI adapter package.

## Code Review Rules

- Keep raw adapter behavior here while relying on the shared `usb` package for
  host transport and ownership.
- Check interface ownership, adapter configuration, deadlines, transfer
  lengths, close ordering, and restoration across every success and failure
  path.
- Do not duplicate host USB mechanisms or move FTDI-specific protocol state
  into commands, examples, or tests.
- For native C or header changes, require format-clean and warning-clean
  compilation under the deployment target and verify safe ownership across
  the Go and C boundary.
- Require platform-independent behavioral coverage where possible and matching
  platform evidence for host-specific behavior.
