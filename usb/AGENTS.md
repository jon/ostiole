# USB implementation guidance

This file supplements the repository-wide agent and code-review rules for the
USB package and its native host bridges.

## Code Review Rules

- Preserve the common `usb` API and supported Linux and macOS behavior.
- Check host handles, interface ownership, alternate settings, deadlines,
  transfer lengths, and close ordering across every success and failure path.
- Keep cgo, IOKit, CoreFoundation, IOUSBLib, usbfs, and raw USB details inside
  this package unless an adapter driver owns the detail.
- Verify C changes remain format-clean, warning-clean under the deployment
  target, and safe across Go and C ownership boundaries.
- Require platform-independent behavior tests where possible and matching
  platform coverage for host-specific implementation changes.
