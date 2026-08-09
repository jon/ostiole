---
applyTo: "usb/**/*.go,usb/**/*.c,usb/**/*.h,ftdi/**/*.go,ftdi/**/*.c,ftdi/**/*.h"
---

# Native USB review rules

- Preserve the common `usb` API and supported Linux and macOS behavior.
- Check host handles, interface ownership, alternate settings, deadlines,
  transfer lengths, and close ordering across every success and failure path.
- Keep cgo, IOKit, CoreFoundation, IOUSBLib, usbfs, and raw USB details inside
  the `usb` package or the adapter driver that owns them.
- Verify C changes remain format-clean, warning-clean under the deployment
  target, and safe across Go and C ownership boundaries.
- Require platform-independent behavior tests where possible and matching
  platform coverage for host-specific implementation changes.
