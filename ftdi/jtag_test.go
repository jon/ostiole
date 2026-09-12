package ftdi

import (
	"context"
	"errors"
	"testing"
)

func TestJTAGCommandClocking(t *testing.T) {
	for _, bits := range []int{1, 7, 8, 9, 63, 64, 65} {
		for _, mask := range []byte{0, 0xff, 0xa5} {
			tms, tdi := make([]byte, (bits+7)/8), make([]byte, (bits+7)/8)
			for i := range tms {
				tms[i], tdi[i] = mask, 0x96
			}
			commands, reads := jtagCommands(tms, tdi, bits)
			ms, di, reply := interpretJTAG(t, commands)
			if len(ms) != bits || len(di) != bits {
				t.Fatal("wrong clock count")
			}
			got := decodeSWD(reply, reads, bits)
			for i := range bits {
				if ms[i] != streamBit(tms, i) || di[i] != streamBit(tdi, i) {
					t.Fatalf("pins differ at bit %d", i)
				}
				if streamBit(got, i) != (i%3 == 0) {
					t.Fatalf("TDO differs at bit %d", i)
				}
			}
		}
	}
}

func TestJTAGRetainsFailedSetupCleanup(t *testing.T) {
	setup, drain := errors.New("setup"), errors.New("drain")
	raw := &probeSetupFailure{fakeUSBDevice: &fakeUSBDevice{abortErr: drain, abortErrEP: 0x02}, setupErr: setup}
	c, err := openChannel(t.Context(), raw, Config{Port: PortA, MaxClockHz: 100_000})
	if c == nil || !errors.Is(err, setup) || !errors.Is(err, drain) {
		t.Fatalf("lost owner: %v %v", c, err)
	}
	if raw.closed {
		t.Fatal("closed without successful drain")
	}
	if c.MaxTransferBits() != 0 || c.ClockHz() != 0 {
		t.Fatal("failed setup exposed an active wire")
	}
	if _, err := c.JTAGIO(t.Context(), []byte{0}, []byte{0}, 1); err == nil {
		t.Fatal("failed setup allowed clocks")
	}
	raw.abortErr = nil
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.MaxTransferBits() != 0 || c.ClockHz() != 0 {
		t.Fatal("closed channel remains active")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestJTAGWorstCaseFitsReceiveWindow(t *testing.T) {
	tms, tdi := make([]byte, maxJTAGTransferBits/8), make([]byte, maxJTAGTransferBits/8)
	for i := range tms {
		tms[i], tdi[i] = 0xff, 0x55
	}
	_, reads := jtagCommands(tms, tdi, maxJTAGTransferBits)
	if len(reads) > (maxSWDTransferBits+1)/2 {
		t.Fatal("response exceeds preposted capacity")
	}
}

func interpretJTAG(t *testing.T, commands []byte) (ms, di []bool, reply []byte) {
	t.Helper()
	tms := false
	for at := 0; at < len(commands); {
		op := commands[at]
		at++
		if op == 0x87 {
			continue
		}
		if op == 0x80 {
			tms = commands[at]&8 != 0
			if commands[at+1] != 0x0b {
				t.Fatal("unsafe directions")
			}
			at += 2
			continue
		}
		if op != 0x3b && op != 0x6b {
			t.Fatalf("unexpected opcode %x", op)
		}
		bits, data := int(commands[at])+1, commands[at+1]
		at += 2
		if bits > 8 || op == 0x6b && bits > 7 {
			t.Fatal("oversized command")
		}
		var samples byte
		for i := range bits {
			out := data>>uint(i)&1 != 0
			if op == 0x6b {
				tms = out
				out = data&0x80 != 0
			}
			if len(ms)%3 == 0 {
				samples |= 1 << uint(8-bits+i)
			}
			ms, di = append(ms, tms), append(di, out)
		}
		reply = append(reply, samples)
	}
	return ms, di, reply
}

func TestJTAGRejectsInvalidStream(t *testing.T) {
	raw := &fakeUSBDevice{}
	c, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	claimFakeChannel(t, c)
	channel := c
	large := make([]byte, (maxJTAGTransferBits+8)/8)
	for _, tc := range []struct {
		bits     int
		tms, tdi []byte
	}{{-1, nil, nil}, {9, []byte{0}, []byte{0, 0}}, {9, []byte{0, 0}, []byte{0}}, {maxJTAGTransferBits + 1, large, large}} {
		if _, err := channel.JTAGIO(t.Context(), tc.tms, tc.tdi, tc.bits); err == nil {
			t.Fatal("invalid stream accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if _, err := channel.JTAGIO(ctx, []byte{0}, []byte{0}, 1); err == nil {
			t.Fatal("invalid context accepted")
		}
	}
	if data, err := channel.JTAGIO(t.Context(), nil, nil, 0); err != nil || len(data) != 0 {
		t.Fatalf("zero-bit no-op: %x %v", data, err)
	}
	if raw.writesN != 0 {
		t.Fatal("preflight sent traffic")
	}
}
