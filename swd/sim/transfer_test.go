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
	connectionTarget
	values map[sim.Request]uint32
	writes map[sim.Request]uint32
	ready  bool
}

func (t *registerTarget) Read(ctx context.Context, req sim.Request) (uint32, error) {
	if !t.ready {
		return t.connectionTarget.Read(ctx, req)
	}
	return t.values[req], nil
}

func (t *registerTarget) Write(ctx context.Context, req sim.Request, value uint32) error {
	if !t.ready {
		return t.connectionTarget.Write(ctx, req, value)
	}
	t.writes[req] = value
	return nil
}

type acknowledgingTarget struct {
	connectionTarget
	ack       error
	ackOnce   bool
	readValue uint32
	reads     int
	writes    int
	ready     bool
}

func (t *acknowledgingTarget) Acknowledge(ctx context.Context, req sim.Request) error {
	if !t.ready {
		return t.connectionTarget.Acknowledge(ctx, req)
	}
	if t.ackOnce {
		t.ackOnce = false
		return t.ack
	}
	return nil
}

func (t *acknowledgingTarget) Read(ctx context.Context, req sim.Request) (uint32, error) {
	if !t.ready {
		return t.connectionTarget.Read(ctx, req)
	}
	t.reads++
	return t.readValue, nil
}

func (t *acknowledgingTarget) Write(ctx context.Context, req sim.Request, value uint32) error {
	if !t.ready || !req.AP {
		return t.connectionTarget.Write(ctx, req, value)
	}
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
			target := &acknowledgingTarget{connectionTarget: connectionTarget{dpidr: 0x2ba01477}, readValue: 0x2ba01477}
			wire := sim.New(target)
			conn := swd.New(wire)
			if _, err := conn.Connect(t.Context()); err != nil {
				t.Fatalf("Connect(): %v", err)
			}
			target.ready = true
			target.ack = test.err
			target.ackOnce = true

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
	target := &acknowledgingTarget{connectionTarget: connectionTarget{dpidr: 0x2ba01477}}
	wire := sim.New(target)
	conn := swd.New(wire)
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	target.ready = true
	target.ack = swd.ErrWait
	target.ackOnce = true
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

func TestConnectionRequiresRepairWhenABORTReturnsWAIT(t *testing.T) {
	target := &acknowledgingTarget{connectionTarget: connectionTarget{dpidr: 0x2ba01477}}
	conn := swd.New(sim.New(target))
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	target.ready = true
	target.ack = swd.ErrWait
	target.ackOnce = true
	if err := conn.WriteDP(t.Context(), 0x00, 0x1e); !errors.Is(err, swd.ErrWait) {
		t.Fatalf("WriteDP(ABORT) error = %v, want WAIT", err)
	}
	if _, err := conn.ReadDP(t.Context(), 0x00); err == nil {
		t.Fatal("ReadDP() succeeded while ABORT cleanup was pending")
	}
	if target.reads != 0 {
		t.Fatalf("target reads after WAITed ABORT = %d, want 0", target.reads)
	}
	target.ack = nil
	target.ready = false
	if err := conn.Release(t.Context()); err != nil {
		t.Fatalf("Release() after WAITed ABORT: %v", err)
	}
}

func TestWireExecutesRegisterTransfers(t *testing.T) {
	read := sim.Request{Read: true}
	write := sim.Request{AP: true, Addr: 0x0c}
	target := &registerTarget{
		connectionTarget: connectionTarget{dpidr: 0x2ba01477},
		values:           map[sim.Request]uint32{read: 0x2ba01477},
		writes:           make(map[sim.Request]uint32),
	}
	conn := swd.New(sim.New(target))
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect(): %v", err)
	}
	target.ready = true
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
		!strings.Contains(err.Error(), "Connect") {
		t.Fatalf("pre-entry transfer error = %v", err)
	}

	conn = swd.New(sim.New(nil))
	if _, err := conn.Connect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "target") {
		t.Fatalf("targetless transfer error = %v", err)
	}
}
