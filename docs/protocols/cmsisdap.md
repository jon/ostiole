# CMSIS-DAP v2 USB sessions and SWD configuration

The `cmsisdap` package implements a CMSIS-DAP v2 USB command session with
metadata and optional SWD port configuration. Arm's
[CMSIS-DAP documentation][commands] defines the command protocol, and its
[USB firmware guidance][usb] defines the v2 USB interface. This note records
the narrower host boundary Ostiole implements.

[commands]: https://arm-software.github.io/CMSIS-DAP/latest/group__DAP__Info.html
[usb]: https://arm-software.github.io/CMSIS-DAP/latest/dap_firmware.html#dap_bulk_usb
[connect]: https://arm-software.github.io/CMSIS-DAP/latest/group__DAP__Connect.html
[clock]: https://arm-software.github.io/CMSIS-DAP/latest/group__DAP__SWJ__Clock.html
[disconnect]: https://arm-software.github.io/CMSIS-DAP/latest/group__DAP__Disconnect.html

## Discovery and interface selection

CMSIS-DAP does not have one vendor and product ID catalog. `usb.AllDevices`
therefore provides the inventory, and `cmsisdap.Candidates` returns the
attachments whose host-visible USB product string contains the case-sensitive
`CMSIS-DAP` marker. That is only a shortlist. A product string does not prove
that a device implements v2, and a composite device may instead put the marker
in an interface string which the current USB package does not expose.

The application still selects one complete `usb.DeviceInfo`. It may explicitly
select a known composite attachment which is absent from `Candidates`.
`cmsisdap.Open` accepts the selected device only when its active descriptors
contain exactly one vendor-specific `ff/00/00` alternate. The endpoints must
appear in the order required by the v2 interface: bulk OUT for commands, bulk
IN for responses, and optionally a distinct bulk IN endpoint for SWO. The
current package records but does not use the optional SWO endpoint.

An HID interface is CMSIS-DAP v1 rather than v2. `Open` rejects it with
`ErrNoV2Interface`; it does not fall back to interrupt transfers.

## Metadata commands

A successful `Open` claims and selects the command interface, resolves the
active endpoints, and sends only `DAP_Info` commands. It reads packet size,
packet count, capabilities, protocol version, vendor, product, serial, and
firmware version. A missing product or serial uses the corresponding USB
string. The USB package does not currently expose a manufacturer string, so a
missing CMSIS-DAP vendor remains empty.

The packet-size query begins with the active bulk IN maximum packet size as a
bootstrap response capacity. Once the probe reports its command packet size,
every later command and response uses that negotiated limit. Packet count is
reported through `Info`, but the session intentionally keeps one command
exchange outstanding; it does not pipeline packets or emit atomic command
groups.

For each command, the package submits the complete response IN request before
submitting the command OUT request. The USB package preserves endpoint
addresses, requested lengths, and completion counts; `cmsisdap` owns this
protocol-specific pairing and buffer size. A failed OUT submission aborts the
already-posted IN request. Once an OUT command may have completed, a transfer
failure, invalid completion count, or progress-free response poisons the
session. The error retains its command phase and matches `ErrSessionPoisoned`.
Recovery is close and explicit reopen; the package never replays a command.

`Open` does not send `DAP_Connect`, select SWD or JTAG, set a clock, or touch a
target. `WithSWD` during open or `ConfigureSWD` afterward requires the
advertised SWD capability, sends [DAP_Connect][connect] for port 1, and gives
[DAP_SWJ_Clock][clock] the caller's nonzero maximum frequency in hertz. A
successful response confirms that the request was accepted; CMSIS-DAP does not
report the attained clock. `MaxClockHz` reports that accepted request.
Reconfiguration disconnects the active port first.

`Close` normally sends [DAP_Disconnect][disconnect] before releasing the USB
interface. If `DAP_Disconnect` returns a complete error response, the session
retains ownership for another attempt. After failed SWD configuration, `Open`
makes another bounded disconnect attempt; if synchronized cleanup remains
pending, the caller receives the non-nil session with the setup error and
retries `Session.Close`. After a poisoned exchange, `Close` reports the
abandoned active port and continues USB cleanup without sending another
command. Device close still runs once and its result is cached.

## Bench observation

The `0d28:0204` BBC micro:bit attached to the macOS bench identifies itself as
`BBC micro:bit CMSIS-DAP` and exposes the v2 bulk command interface. The HIL
selected it from the product-string shortlist, opened and closed one metadata
session, then opened a second. Both sessions reported protocol `2.1.0`,
firmware `0257`, packet size 64, packet count 5, and capabilities `0x11`. They
sent no `DAP_Connect` or target traffic.

The DAPLink `0d28:0204` attached to the Linux bench identifies itself as
`DAPLink CMSIS-DAP`, but its command interface is HID. Its other
vendor-specific interface has subclass 3 and no endpoints. The product-string
inventory reported its product and serial. The HIL selected it by serial, and
the v2 opener rejected it before claiming an interface. No CMSIS-DAP command
or target traffic was sent.

Neither bench exercises CMSIS-DAP SWD configuration, JTAG, SWO, or a target
connection.
