package sim_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/swd/sim"
)

type registerTarget struct {
	values map[sim.Request]uint32
	writes map[sim.Request]uint32
}

func (t *registerTarget) Read(_ context.Context, req sim.Request) (uint32, error) {
	return t.values[req], nil
}

func (t *registerTarget) Write(_ context.Context, req sim.Request, value uint32) error {
	t.writes[req] = value
	return nil
}

type acknowledgingTarget struct {
	ack       error
	readValue uint32
	reads     int
	writes    int
}

func (t *acknowledgingTarget) Acknowledge(context.Context, sim.Request) error {
	return t.ack
}

func (t *acknowledgingTarget) Read(context.Context, sim.Request) (uint32, error) {
	t.reads++
	return t.readValue, nil
}

func (t *acknowledgingTarget) Write(context.Context, sim.Request, uint32) error {
	t.writes++
	return nil
}

func TestWireReturnsAcknowledgementBeforeExecutingTransfer(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "WAIT", err: swd.ErrWait},
		{name: "FAULT", err: swd.ErrFault},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := &acknowledgingTarget{ack: test.err, readValue: 0x2ba01477}
			wire := sim.New(target)
			conn := swd.New(wire)
			if err := conn.JTAGToSWD(t.Context()); err != nil {
				t.Fatal(err)
			}

			_, err := conn.ReadDP(t.Context(), 0x00)
			if !errors.Is(err, test.err) {
				t.Fatalf("ReadDP() error = %v, want %v", err, test.err)
			}
			if target.reads != 0 {
				t.Fatalf("target reads = %d, want 0", target.reads)
			}

			target.ack = nil
			value, err := conn.ReadDP(t.Context(), 0x00)
			if err != nil {
				t.Fatal(err)
			}
			if value != target.readValue || target.reads != 1 {
				t.Fatalf("read = %#08x after %d target reads", value, target.reads)
			}
		})
	}
}

func TestWireDoesNotExecuteWAITedWrite(t *testing.T) {
	target := &acknowledgingTarget{ack: swd.ErrWait}
	wire := sim.New(target)
	conn := swd.New(wire)
	if err := conn.JTAGToSWD(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteAP(t.Context(), 0x0c, 0x12345678); !errors.Is(err, swd.ErrWait) {
		t.Fatalf("WriteAP() error = %v, want %v", err, swd.ErrWait)
	}
	if target.writes != 0 {
		t.Fatalf("target writes = %d, want 0", target.writes)
	}
	target.ack = nil
	if err := conn.WriteAP(t.Context(), 0x0c, 0x12345678); err != nil {
		t.Fatal(err)
	}
	if target.writes != 1 {
		t.Fatalf("target writes = %d, want 1", target.writes)
	}
}

func TestWireExecutesRegisterTransfers(t *testing.T) {
	read := sim.Request{Read: true}
	write := sim.Request{AP: true, Addr: 0x0c}
	target := &registerTarget{
		values: map[sim.Request]uint32{read: 0x2ba01477},
		writes: make(map[sim.Request]uint32),
	}
	conn := swd.New(sim.New(target))
	if err := conn.JTAGToSWD(t.Context()); err != nil {
		t.Fatalf("JTAGToSWD: %v", err)
	}
	got, err := conn.ReadDP(t.Context(), 0x00)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != target.values[read] {
		t.Fatalf("read = %#08x", got)
	}
	const value = 0x12345678
	if err := conn.WriteAP(t.Context(), 0x0c, value); err != nil {
		t.Fatalf("write: %v", err)
	}
	if target.writes[write] != value {
		t.Fatalf("write = %#08x", target.writes[write])
	}
}

func TestWireRequiresProtocolEntryAndATarget(t *testing.T) {
	conn := swd.New(sim.New(&registerTarget{}))
	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil ||
		!strings.Contains(err.Error(), "entry") {
		t.Fatalf("pre-entry transfer error = %v", err)
	}

	conn = swd.New(sim.New(nil))
	if err := conn.LineReset(t.Context()); err != nil {
		t.Fatalf("line reset: %v", err)
	}
	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil ||
		!strings.Contains(err.Error(), "target") {
		t.Fatalf("targetless transfer error = %v", err)
	}
}
