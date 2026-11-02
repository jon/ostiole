# Linux USB Access

Ostiole uses sysfs for discovery and usbfs for transfers on Linux. Hardware
commands should run as an unprivileged user. Do not work around device
permissions with `sudo go run` or `sudo go test`: those commands compile and
execute repository code as root.

Discovery skips sysfs entries whose required identity or bus/address attributes
are missing, including entries returning `ENODEV` during removal. Permission and
other I/O failures remain errors. If the selected usbfs node disappears during
open, the error matches `usb.ErrStaleCandidate` and preserves the native open
error. Opening is not replayed and does not trigger another inventory scan.

`ActiveConfiguration` reads configuration descriptor bytes from the already
opened attachment's usbfs file. This avoids the control ioctl's host-page-size
limit for large configurations. The active value is still queried before and
after inspection. Malformed, truncated or inconsistent descriptor data fails
without opening another attachment or falling back to a control transfer. An
`ENODEV` during either cached descriptor read matches `usb.ErrDeviceGone` and
preserves the native cause; other read errors retain their identity.

Standard device, configuration, interface and endpoint descriptors retain their
defined fields while ignoring legal trailing bytes. Parsing advances by the
returned descriptor length. Zero-bandwidth isochronous alternates may report a
zero maximum packet size; bulk endpoints still require a nonzero payload size.
After a successful alternate selection, endpoint lookup reloads its descriptors
using the known selection without another `GET_INTERFACE` request.

The control API accepts buffers up to the USB limit of 65,535 bytes; Linux usbfs
can reject a request larger than the host page size. The host timeout defaults
to five seconds; a context deadline rounds up to the next millisecond and clamps
to the Linux unsigned 32-bit millisecond limit. Control requests use the native
pointer width, and their buffers remain pinned until the ioctl returns.
Interface and bulk requests use the ioctl direction encoding for the host
architecture, including the PowerPC and MIPS variants. This ABI handling does
not establish physical validation on every Linux architecture.

## Grant device access

Give the interactive user permission to open only the intended USB products. For
the FT232H used by the examples, a system using systemd-logind can install this
udev rule as `/etc/udev/rules.d/70-ostiole-ftdi.rules`:

```udev
SUBSYSTEM=="usb", ATTR{idVendor}=="0403", ATTR{idProduct}=="6014", MODE="0660", TAG+="uaccess"
```

Reload the rules with `sudo udevadm control --reload-rules`, then unplug and
reconnect the adapter.

A headless bench should run HIL under an unprivileged, dedicated account in a
narrowly chosen device-access group. For example, if that account is in
`plugdev`, use:

```udev
SUBSYSTEM=="usb", ATTR{idVendor}=="0403", ATTR{idProduct}=="6014", MODE="0660", GROUP="plugdev"
```

Add rules only for the exact USB products the bench uses.

## Release a bound FTDI interface

Device-node permission is necessary but not sufficient when a kernel driver owns
the interface. Ostiole does not currently detach kernel drivers, and `ftdi_sio`
normally binds the FT232H. In that state, opening the usbfs node succeeds but
claiming the interface returns `EBUSY`.

Before releasing a driver, confirm that no serial process is using the exact
adapter. `go run ./cmd/ost ftdi list` reports its USB bus and address. Query the
corresponding node to find its current sysfs path:

```sh
udevadm info --query=path --name=/dev/bus/usb/001/007
```

For an FT232H on a path ending in `1-1.2`, MPSSE port A is interface
`1-1.2:1.0`. Replace that example with the current path, then run one hardware
command in a restoration-bounded subshell:

```sh
(
  set -eu
  interface=1-1.2:1.0
  driver=/sys/bus/usb/drivers/ftdi_sio
  unbound=0
  restore_driver() {
    restore_rc=$?
    trap - EXIT
    if [ "$unbound" -eq 1 ] &&
      ! printf '%s' "$interface" | sudo tee "$driver/bind" >/dev/null
    then
      if [ "$restore_rc" -eq 0 ]; then
        restore_rc=1
      fi
    fi
    exit "$restore_rc"
  }
  trap restore_driver EXIT
  printf '%s' "$interface" | sudo tee "$driver/unbind" >/dev/null || exit $?
  unbound=1
  go run ./examples/trivial/swd-dpidr
)
```

The elevated operations act only on the exact kernel-driver interface;
repository code still runs unprivileged. The trap rebinds only after this
subshell successfully unbound the interface, preserves a failed hardware
command's status, and reports restoration failure after a successful command.

Never reuse a sysfs interface name after reconnecting the adapter without
identifying it again.

## Cleanup after disappearance

After a device disappears, Linux may return `ENODEV` when releasing its
interface. The claim is finished, and `Device.Close` continues closing the usbfs
file while returning the disappearance error. Pending bulk transfers are drained
before interface release. A later close does not repeat the interface-release
request. Other release errors retain the claim and file for a retry.
