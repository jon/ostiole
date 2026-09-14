package ftdi

import (
	"context"
	"errors"
	"testing"
)

func TestOpenCanceledLeavesAttachmentWithCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		raw := &fakeUSBDevice{}
		c, err := openChannel(ctx, raw, Config{Port: PortA, MaxClockHz: 100_000})
		if c != nil || err == nil || raw.closed || len(raw.controls) != 0 {
			t.Fatal("preflight took ownership")
		}
	}
}

func TestOpenRetainsCleanup(t *testing.T) {
	setup, cleanup := errors.New("setup"), errors.New("cleanup")
	raw := &probeSetupFailure{fakeUSBDevice: &fakeUSBDevice{abortErr: cleanup, abortErrEP: 0x02}, setupErr: setup}
	c, err := openChannel(t.Context(), raw, Config{Port: PortA, MaxClockHz: 100_000})
	if c == nil || !errors.Is(err, setup) || !errors.Is(err, cleanup) {
		t.Fatalf("lost owner: %v", err)
	}
	if c.MaxTransferBits() != 0 {
		t.Fatal("failed setup permits transfers")
	}
	raw.abortErr = nil
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestJTAGSetsDirectionsBeforeFirstHighTMS(t *testing.T) {
	commands, _ := jtagCommands([]byte{1}, []byte{0}, 1)
	if len(commands) < 3 || commands[0] != cmdSetDataLow || commands[1] != 0 || commands[2] != 0x0b {
		t.Fatalf("missing pin setup: %x", commands)
	}
}

func TestWireMethodsShareEndpoint(t *testing.T) {
	raw := &fakeUSBDevice{}
	c, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	claimFakeChannel(t, c)
	if _, err := c.SWDIO(t.Context(), []byte{1}, []byte{0}, 1); err != nil {
		t.Fatal(err)
	}
	raw.mu.Lock()
	reads := raw.readsN
	raw.mu.Unlock()
	if reads != 0 {
		t.Fatalf("write-only SWD completed %d USB reads", reads)
	}
	// The fake releases queued replies on the next USB write.
	raw.mu.Lock()
	raw.readData = [][]byte{{0x01, 0x60, 0x80}}
	raw.mu.Unlock()
	got, err := c.JTAGIO(t.Context(), []byte{1}, []byte{0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("JTAG sample = %x, want 01", got)
	}
	if _, err := c.SWDIO(t.Context(), []byte{1}, []byte{0}, 1); err != nil {
		t.Fatal(err)
	}
	if len(raw.writes) != 3 || raw.writes[1][2] != 0x0b || raw.writes[2][2] != 0x03 {
		t.Fatal("directions did not follow calls")
	}
}

func TestBothWiresRejectInvalidAndClosedCalls(t *testing.T) {
	raw := &fakeUSBDevice{}
	c, err := newChannel(raw, Config{Port: PortA, MaxClockHz: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	claimFakeChannel(t, c)
	for _, transfer := range []func(context.Context, []byte, []byte, int) ([]byte, error){c.SWDIO, c.JTAGIO} {
		if _, err := transfer(t.Context(), nil, nil, -1); err == nil {
			t.Fatal("negative length accepted")
		}
		if data, err := transfer(t.Context(), nil, nil, 0); err != nil || len(data) != 0 {
			t.Fatal("zero call was not a no-op")
		}
		large := make([]byte, (c.MaxTransferBits()+8)/8)
		if _, err := transfer(t.Context(), large, large, c.MaxTransferBits()+1); err == nil {
			t.Fatal("oversized call accepted")
		}
	}
	if raw.writesN != 0 {
		t.Fatal("invalid or zero call sent clocks")
	}
	raw.abortErr, raw.abortErrEP = errors.New("drain"), 0x02
	if err := c.Close(); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	for _, transfer := range []func(context.Context, []byte, []byte, int) ([]byte, error){c.SWDIO, c.JTAGIO} {
		if _, err := transfer(t.Context(), []byte{1}, []byte{1}, 1); err == nil {
			t.Fatal("close did not block clocks")
		}
	}
	raw.abortErr = nil
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
