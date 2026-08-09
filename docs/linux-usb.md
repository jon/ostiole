# Linux USB Access

Ostiole uses sysfs for discovery and usbfs for transfers on Linux. Hardware
commands should run as an unprivileged user. Do not work around device
permissions with `sudo go run` or `sudo go test`: those commands compile and
execute repository code as root.

## Grant device access

Give the interactive user permission to open only the intended USB products.
For the FT232H used by the examples, a system using systemd-logind can install
this udev rule as `/etc/udev/rules.d/70-ostiole-ftdi.rules`:

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

Device-node permission is necessary but not sufficient when a kernel driver
owns the interface. Ostiole does not currently detach kernel drivers, and
`ftdi_sio` normally binds the FT232H. In that state, opening the usbfs node
succeeds but claiming the interface returns `EBUSY`.

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
    status=$?
    trap - EXIT
    if [ "$unbound" -eq 1 ] &&
      ! printf '%s' "$interface" | sudo tee "$driver/bind" >/dev/null
    then
      if [ "$status" -eq 0 ]; then
        status=1
      fi
    fi
    exit "$status"
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
