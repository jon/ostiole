# CMSIS-DAP v2 USB and SWD sessions

The `cmsisdap` package implements CMSIS-DAP v2 metadata and direction-explicit
SWD over one USB command session. Arm's [CMSIS-DAP documentation][commands]
defines the command protocol, and its [USB firmware guidance][usb] defines the
v2 USB interface. This note records the narrower host boundary Ostiole
implements.

[commands]:
  https://arm-software.github.io/CMSIS-DAP/latest/group__DAP__Info.html
[usb]:
  https://arm-software.github.io/CMSIS-DAP/latest/dap_firmware.html#dap_bulk_usb
[connect]:
  https://arm-software.github.io/CMSIS-DAP/latest/group__DAP__Connect.html
[clock]:
  https://arm-software.github.io/CMSIS-DAP/latest/group__DAP__SWJ__Clock.html
[sequence]:
  https://arm-software.github.io/CMSIS-DAP/latest/group__DAP__SWD__Sequence.html
[disconnect]:
  https://arm-software.github.io/CMSIS-DAP/latest/group__DAP__Disconnect.html

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
appear in the order required by the v2 interface: bulk OUT for commands, bulk IN
for responses, and optionally a distinct bulk IN endpoint for SWO. The current
package records but does not use the optional SWO endpoint.

An HID interface is CMSIS-DAP v1 rather than v2. `Open` rejects it with
`ErrNoV2Interface`; it does not fall back to interrupt transfers.

## Metadata commands

A successful `Open` claims and selects the command interface, resolves the
active endpoints, and sends only `DAP_Info` commands. It reads packet size,
packet count, capabilities, protocol version, vendor, product, serial, and
firmware version. A missing product or serial uses the corresponding USB string.
The USB package does not currently expose a manufacturer string, so a missing
CMSIS-DAP vendor remains empty.

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

With no options, `Open` does not send `DAP_Connect`, select SWD or JTAG, set a
clock, or touch a target.

## SWD connection and sequences

`WithSWD` during open or `ConfigureSWD` afterward first requires protocol 1.2 or
later and the SWD capability reported by `DAP_Info`. It sends
[DAP_Connect][connect] with port 1, then sends the caller's requested maximum
frequency as the little-endian hertz value in [DAP_SWJ_Clock][clock]. A
successful clock response says that the request was accepted; CMSIS-DAP does not
report the rate the probe attained. Reconfiguring an active session disconnects
it first.

The configured session provides explicit sequences through
[DAP_SWD_Sequence][sequence], available in CMSIS-DAP 1.2 and later. For each set
direction bit, the probe drives SWDIO; for each clear bit, it samples SWDIO.
Each run carries at most 64 cycles, with 64 encoded as zero; data is packed
least-significant bit first. Output bytes are absent from input sequences, so a
caller's output bits are never driven while the target owns SWDIO.

One command can contain several runs. The driver preflights the entire call,
keeping every request and response within the negotiated packet size and every
command within the 255-sequence limit. A target-driven phase must fit entirely
in one command, even when it needs several 64-cycle descriptors. A call must end
with host ownership. An unsafe plan is rejected without sending any command.
Arm's [reference firmware][firmware] enables SWDIO output at each command's end;
splitting a target-driven phase would therefore drive against the target.

These restrictions mean that `Session` cannot serve as a general `swd.Wire`. Do
not compose it with `swd.New`. `OpenProbe` acquires only the USB attachment; its
`SWD` and `JTAG` calls return the corresponding unsupported-capability error
before claiming or configuring an interface. Direct `Open` remains metadata-only
unless configured explicitly. One safe `SWDIO` call may contain several ordered,
unpipelined commands, up to 16,384 bits. Commands are never replayed.

[firmware]:
  https://github.com/ARM-software/CMSIS-DAP/blob/main/Firmware/Source/DAP.c

Cancellation before a command is submitted sends no target traffic. After the
first OUT is accepted, the remaining OUT/IN completions and all subsequent
packets share an independent one-second budget. Increase it with
`cmsisdap.WithCompletionTimeout(5*time.Second)` when opening slower sessions;
nonpositive values are rejected before interface claim or command traffic.
Caller cancellation is returned after the physical outcome has been recorded. A
non-nil `SWDIO` capture means that the entire stream completed, even when the
error also reports cancellation. A confirmed final clock response likewise
updates configuration before `ConfigureSWD` returns cancellation. Failed open
then attempts cleanup of the state actually acquired.

A complete command rejection before any earlier sequence packet has run leaves
the session usable. If an earlier packet has clocked a prefix, any later failure
poisons the session so another operation cannot complete that prefix with
unintended bits. Wrong command IDs, missing captured data, and ambiguous USB
exchanges also poison the session. Close and explicit reopen are required; no
failed operation is replayed.

`Close` normally sends [DAP_Disconnect][disconnect] before releasing the USB
interface. If `DAP_Disconnect` returns a complete error response, the session
retains ownership for another attempt. After failed SWD configuration, `Open`
makes another bounded disconnect attempt; if synchronized cleanup remains
pending, the caller receives the non-nil session with the setup error and
retries `Session.Close`. After a poisoned exchange, `Close` reports the
abandoned active port and continues USB cleanup without sending another command.
Known USB connection loss matches `usb.ErrDeviceGone` and also makes port
restoration impossible. An unsent timeout, cancellation, or other live
submission failure retains the session for retry. Once a command may have
completed, an ambiguous exchange still poisons the stream; commands are never
replayed.

`Session.RestorationPossible()` and `Session.CleanupPending()` implement the
optional probe state contracts without I/O. Restoration can be impossible while
host cleanup remains pending. A live claim or device cleanup failure retains
ownership for another `Close`, including after failed open. Final claim and
device errors remain visible on the attempt which releases them. Once all
resources are released, further session closes return nil without traffic.
Release borrowed target and DAP owners before closing the session; freeing host
resources does not restore a halted target.

## Historical bench observations

The target-access observations below used the earlier unrestricted sequence
adapter. They do not establish target access through the current safe sequence
surface. Metadata-only reopen and passive v1 rejection remain applicable.

The `0d28:0204` BBC micro:bit attached to the macOS bench identifies itself as
`BBC micro:bit CMSIS-DAP` and exposes the v2 bulk command interface. The HIL
selected it from the product-string shortlist, opened and closed one metadata
session, then opened a second. Both sessions reported protocol `2.1.0`, firmware
`0257`, packet size 64, packet count 5, and capabilities `0x11`. They sent no
`DAP_Connect` or target traffic.

Two fresh read-only sessions selected the same serial and requested a 100 kHz
maximum clock. One called `ConfigureSWD` after metadata-only open; the other
used `WithSWD`. Both returned DPIDR `0x0bb11477`, AP0 IDR `0x04770021`, CPUID
`0x410cc200`, and DHCSR `0x01000001`. Each saved and restored AP0 CSW and TAR,
released the debug port, disconnected the CMSIS-DAP session, and reproduced the
identities and `DHCSR.S_HALT` state after reopen. Each session sent 20 packed
SWD frames in 57 `SWDIO` calls.

OpenOCD 0.12.0 independently selected the same serial and v2 bulk interface at
100 kHz. It returned the same DPIDR and AP0 IDR and identified the target as
Cortex-M0. These observations cover one probe, one target, read-only access, and
restoration of the session and target state. They do not establish the clock the
probe attained or validate JTAG or SWO.

The DAPLink `0d28:0204` attached to the Linux bench identifies itself as
`DAPLink CMSIS-DAP`, but its command interface is HID. Its other vendor-specific
interface has subclass 3 and no endpoints. The product-string inventory reported
its product and serial. The HIL selected it by serial, and the v2 opener
rejected it before claiming an interface. No CMSIS-DAP command or target traffic
was sent.

The Linux bench still exercises only passive v1 rejection.

## nRF51 startup clock

The nRF51 on the micro:bit requires at least 125 kHz when entering debug
interface mode after power-on. Nordic also specifies at least 150 SWCLK cycles
with SWDIO high to guarantee that the DAP captures 50 cycles while its power
domain starts; see section 11.1.2 of the [nRF51 reference manual][nrf51-manual].
A slower clock can work once another debugger has activated the interface. The
CMSIS-DAP driver accepts the caller's clock ceiling; it does not know the
target's startup requirements or silently raise that ceiling.

On Nostalgia, Ostiole connected first after a physical micro:bit replug at 1 MHz
and passed both read-only sessions. At 100 kHz, both Ostiole and OpenOCD 0.12.0
failed to read DPIDR after replug, though Ostiole passed after OpenOCD activated
the interface at 1 MHz. Only the test's requested clock changed; the CMSIS-DAP
driver was unchanged. DPIDR was `0x0bb11477`, AP0 IDR was `0x04770021`, and
CPUID was `0x410cc200`. Both sessions restored AP0 CSW and TAR and closed their
owners.

The historical read-only HIL requested 1 MHz.

The current integration suite retains metadata-only reopen and passive v1
rejection. It does not offer the earlier target-restoration procedure.

The startup observation used a physical unplug/replug before the test, with
other debuggers stopped. Reopening a session alone does not reproduce power-on
state. The earlier 100 kHz evidence above covers an active interface; it does
not establish startup from power-on. The new result covers one board and
firmware revision, with no measurement of the attained clock or guarantee of
target-specific activation timing on other devices.

[nrf51-manual]:
  https://docs-be.nordicsemi.com/bundle/nRF51-Series/raw/resource/enus/nRF51_RM_v3.0.1.pdf
