# CMSIS-DAP USB discovery

CMSIS-DAP does not have one vendor and product ID catalog. Arm's [USB firmware
guidance][usb] instead requires the case-sensitive `CMSIS-DAP` marker in a
probe's USB product string, or in an interface string for a composite device.
This note records the narrower discovery boundary Ostiole implements.

[usb]: https://arm-software.github.io/CMSIS-DAP/latest/dap_firmware.html#dap_bulk_usb

Applications explicitly request the broad inventory through `usb.AllDevices`.
`cmsisdap.Candidates` returns a detached shortlist of attachments whose
host-visible product string contains the marker. A product match is not proof
that an attachment implements CMSIS-DAP or that it uses the v2 bulk transport.
The application must still select one complete `usb.DeviceInfo` before opening
the attachment.

A composite device may put the marker only in an interface string, which the
current USB package does not expose. An application which knows such a probe by
serial or another explicit policy can select it from the unfiltered inventory
without passing through `Candidates`.

## Bench observation

The Linux inventory found an attached DAPLink `0d28:0204` by product
`DAPLink CMSIS-DAP` and serial. Its command interface is HID, while its other
vendor-specific interface has subclass 3 and no endpoints. This establishes
the candidate convention on that attachment, not a CMSIS-DAP v2 session or
target connection. The inventory request sent no probe or target command.
