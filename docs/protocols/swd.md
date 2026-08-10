# Serial Wire Debug

Serial Wire Debug (SWD) uses one clock and one bidirectional data signal. The
packet format is small; most of the trouble is knowing who owns SWDIO on each
clock and remembering that everything goes least-significant bit first.

The specification is Arm
[IHI 0031H, _Arm Debug Interface Architecture Specification ADIv5.0 to
ADIv5.2_](https://developer.arm.com/documentation/ihi0031/h). Use chapter B4
and section B5.2 for the protocol definition. This note is limited to details
which are easy to misread and observations from hardware. It covers the
point-to-point protocol, not SWD protocol version 2 target selection,
multidrop, or dormant-state entry.

## A transfer

IHI 0031H sections B4.1 and B4.2 define the complete transfer. Every transfer
begins with an eight-bit request sent by the host:

| Wire bit | Name | Value |
| ---: | --- | --- |
| 0 | Start | 1 |
| 1 | APnDP | 0 for DP, 1 for AP |
| 2 | RnW | 0 for write, 1 for read |
| 3 | A2 | Register address bit 2 |
| 4 | A3 | Register address bit 3 |
| 5 | Parity | Even parity over APnDP, RnW, A2, and A3 |
| 6 | Stop | 0 |
| 7 | Park | 1 |

For example, a DPIDR read is `0xa5` on the host side. It is clocked bit 0
first, so the wire sees `1 0 1 0 0 1 0 1`.

The host releases SWDIO for one turnaround clock and the target returns a
three-bit acknowledgement. The default turnaround is one clock, although
DLCR can describe another value on implementations which support it.

The acknowledgement notation in IHI 0031H is easy to misread. The prose calls
OK `0b001`, but Table B4-1 prints OK as `0b100` under the heading `ACK[0:2]`.
Those are the same three bits viewed in opposite ways: the numeric value is
sent least-significant bit first.

| Response | Numeric value | Bits seen on SWDIO |
| --- | ---: | --- |
| OK | `0b001` | `1 0 0` |
| WAIT | `0b010` | `0 1 0` |
| FAULT | `0b100` | `0 0 1` |

After OK, a read continues directly into 32 data bits and one parity bit from
the target. The target then releases SWDIO for a turnaround clock. A write has
the opposite handoff: after ACK, the target releases SWDIO, then the host sends
32 data bits and parity. Data and parity use the same even-parity convention as
the request.

If the host is going to stop SWCLK after a transfer, IHI 0031H requires at
least eight idle clocks while the host drives SWDIO low. Starting the next
request immediately is also valid.

## WAIT, FAULT, and overrun detection

WAIT means that the request was not accepted and can normally be tried again.
FAULT means that a sticky error has been recorded. An acknowledgement other
than OK, WAIT, or FAULT is a protocol error, not a fourth response code.

There is one important change when `CTRL/STAT.ORUNDETECT` is set. With overrun
detection disabled, WAIT and FAULT end after the acknowledgement and trailing
turnaround. With it enabled, every response has a data phase, including WAIT
and FAULT. A host cannot turn on ORUNDETECT as a register-level feature and
keep using the simpler transfer grammar; it will lose alignment on the first
non-OK response.

The specification says to retry read data after a parity error. That advice is
less mechanical for an AP read because AP reads are posted: by the time parity
fails, the AP pipeline may already have advanced. Recovery has to account for
the Debug Access Port state, not just replay the eight-bit SWD request.

## Getting into SWD

IHI 0031H section B4.3.3 defines connection and line reset. A line reset is at
least 50 clocks with SWDIO high followed by at least two idle clocks. It puts
the SWD interface into its reset state; it is not a reset of every DP
register. DPIDR is the ordinary transaction which leaves that state.

There is a slightly nasty qualification in section B4.3.3: detection of the
50-high sequence is guaranteed while the target is waiting for a packet
header, but is implementation-defined at other points in a transfer. If the
first DPIDR read does not answer, the specified recovery is to send the reset
sequence again. One line reset is not proof that a confused target saw it as
one.

An SWJ-DP can power up in JTAG mode. The recommended JTAG-to-SWD sequence is:

```text
at least 50 high clocks
0xe79e, least-significant bit first
at least 50 high clocks
```

The second high run leaves SWD in line-reset state. Arm points out that the
two low idle clocks from a normal line reset are absent from the switching
figure; a host can supply idle clocks before reading DPIDR.

Multidrop SWD has another boundary worth stating plainly: there is no generic
way to ask an unselected multidrop bus which target IDs are present. The host
must already know which IDs to try. That is a protocol limitation, not a
missing discovery trick.

## Bench note, 2026-08-09

I ran the existing SWD and DAP integration tests on macOS 26.5.2 (arm64),
through an FT232H (`0403:6014`) using MPSSE port A at a requested 400 kHz. The
adapter was wired as follows:

| FT232H signal | Target signal |
| --- | --- |
| D0 | SWCLK |
| D1 through a 1 kΩ series resistor | SWDIO |
| D2 | SWDIO |
| GND | GND |

D1 drove SWDIO and D2 sampled the same line. The target board and SoC names
were not recorded. Its debug fingerprint identifies an ADIv5 MEM-AP and an
otherwise unidentified Cortex-M4:

```text
DPIDR  = 0x2ba01477
AP0 IDR = 0x24770011
AP0 CSW = 0x23000040
CPUID   = 0x410fc241
```

The complete command was:

```sh
OSTIOLE_FTDI_HIL=1 go test -count=1 -p 1 -tags integration -v ./swd ./dap
```

`-count=1` keeps the Go test cache out of the result. `-p 1` prevents the two
package test binaries from competing for the one FTDI interface.

The tests did not reset or halt the target, write target memory, or change
persistent adapter state. This was a host-level end-to-end test, not a logic
analyzer capture. It establishes that this adapter and target completed the
transactions; it does not establish the physical turnaround margin or show a
real WAIT, FAULT, or parity-error waveform.

Measuring turnaround requires a capture of SWCLK, SWDIO, and the FTDI
direction pin. Exercising WAIT, FAULT, and bad parity requires a controllable
target; successful transfers cannot settle those recovery paths.
