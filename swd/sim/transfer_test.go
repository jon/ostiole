package sim_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/swd/sim"
)

type registerTarget struct {
	values map[swd.Request]uint32
	writes map[swd.Request]uint32
}

func (t *registerTarget) Read(
	_ context.Context,
	req swd.Request,
) (uint32, error) {
	return t.values[req], nil
}

func (t *registerTarget) Write(
	_ context.Context,
	req swd.Request,
	value uint32,
) error {
	t.writes[req] = value
	return nil
}

func TestWireExecutesRegisterTransfers(t *testing.T) {
	read := swd.Request{Read: true}
	write := swd.Request{AP: true, Addr: 0x0c}
	target := &registerTarget{
		values: map[swd.Request]uint32{read: 0x2ba01477},
		writes: make(map[swd.Request]uint32),
	}
	conn := swd.New(sim.New(target))
	if err := conn.JTAGToSWD(t.Context()); err != nil {
		t.Fatalf("JTAGToSWD: %v", err)
	}
	got, err := conn.Transfer(t.Context(), read, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != target.values[read] {
		t.Fatalf("read = %#08x", got)
	}
	const value = 0x12345678
	if _, err := conn.Transfer(t.Context(), write, value); err != nil {
		t.Fatalf("write: %v", err)
	}
	if target.writes[write] != value {
		t.Fatalf("write = %#08x", target.writes[write])
	}
}

func TestWireRequiresProtocolEntryAndATarget(t *testing.T) {
	req := swd.Request{Read: true}
	conn := swd.New(sim.New(&registerTarget{}))
	if _, err := conn.Transfer(t.Context(), req, 0); err == nil ||
		!strings.Contains(err.Error(), "entry") {
		t.Fatalf("pre-entry transfer error = %v", err)
	}

	conn = swd.New(sim.New(nil))
	if err := conn.LineReset(t.Context()); err != nil {
		t.Fatalf("line reset: %v", err)
	}
	if _, err := conn.Transfer(t.Context(), req, 0); err == nil ||
		!strings.Contains(err.Error(), "target") {
		t.Fatalf("targetless transfer error = %v", err)
	}
}
