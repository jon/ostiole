package jtag

import (
	"context"
	"errors"
	"testing"
)

// chainWire models capture, shift, update and reset, rather than returning
// precomputed responses to a particular host command sequence.
type chainWire struct {
	taps               []modelTAP
	state              State
	register           []byte
	calls, limit, fail int
	stuck              *byte
}
type modelTAP struct {
	ir          int
	id          uint32
	instruction uint64
	badCapture  bool
}

func (w *chainWire) MaxTransferBits() int { return w.limit }
func (w *chainWire) JTAGIO(_ context.Context, tms, tdi []byte, bits int) ([]byte, error) {
	w.calls++
	if w.fail == w.calls {
		return nil, errors.New("wire failed")
	}
	if bits > w.limit {
		panic("oversized transfer")
	}
	output := make([]byte, (bits+7)/8)
	for i := range bits {
		value := w.tick(get(tms, i), get(tdi, i))
		if w.stuck != nil {
			value = *w.stuck
		}
		put(output, i, value)
	}
	return output, nil
}

func (w *chainWire) tick(tms, tdi byte) byte {
	var out byte
	switch w.state {
	case Reset:
		for i := range w.taps {
			w.taps[i].instruction = 0xe
			if w.taps[i].id == 0 {
				w.taps[i].instruction = ^uint64(0)
			}
		}
	case CaptureIR, CaptureDR:
		w.capture()
	case ShiftIR, ShiftDR:
		if len(w.register) > 0 {
			out = w.register[0]
			copy(w.register, w.register[1:])
			w.register[len(w.register)-1] = tdi
		} else {
			out = tdi
		}
	case UpdateIR:
		offset := 0
		for i := range w.taps {
			w.taps[i].instruction = 0
			for bit := range w.taps[i].ir {
				w.taps[i].instruction |= uint64(w.register[offset+bit]) << uint(bit)
			}
			offset += w.taps[i].ir
		}
	}
	w.state = transitions[w.state][tms]
	return out
}

func (w *chainWire) capture() {
	w.register = nil
	for _, tap := range w.taps {
		bits, value := 1, uint64(0)
		if w.state == CaptureIR {
			bits, value = tap.ir, 1
			if tap.badCapture {
				value = 0
			}
		} else if tap.instruction == 0xe && tap.id != 0 {
			bits, value = 32, uint64(tap.id)
		}
		for i := range bits {
			w.register = append(w.register, byte(value>>uint(i)&1))
		}
	}
}

func TestDiscoverIDAndBypass(t *testing.T) {
	w := &chainWire{state: Idle, limit: 7, taps: []modelTAP{{ir: 4}, {ir: 12, id: 0x14730093}}}
	c := New(w)
	got, err := c.Discover(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Bypass || got[0].IDCODE != 0 || got[1].IDCODE != 0x14730093 || got[1].Bypass {
		t.Fatalf("discovered %+v", got)
	}
	if c.State() != Idle {
		t.Fatal("discovery did not finish in idle")
	}
	w.taps[0].id = 0x5ba00477
	got, err = c.Discover(context.Background(), 4)
	if err != nil || got[0].IDCODE != 0x5ba00477 {
		t.Fatalf("fresh capture: %+v, %v", got, err)
	}
}

func TestDiscoveryBounds(t *testing.T) {
	ctx := context.Background()
	w := &chainWire{state: Reset, limit: 8, taps: []modelTAP{{ir: 4}, {ir: 4}}}
	c := New(w)
	if _, err := c.Discover(ctx, 0); err == nil || w.calls != 0 {
		t.Fatal("invalid bound sent traffic")
	}
	got, err := c.Discover(ctx, 1)
	if !errors.Is(err, ErrDiscoveryLimit) || len(got) != 1 {
		t.Fatalf("bound: %+v, %v", got, err)
	}
	zero, one := byte(0), byte(1)
	w.stuck = &zero
	if _, err := c.Discover(ctx, 4); !errors.Is(err, ErrDiscoveryLimit) {
		t.Fatal(err)
	}
	w.stuck = &one
	if _, err := c.Discover(ctx, 4); !errors.Is(err, ErrNoChain) {
		t.Fatal(err)
	}
}
