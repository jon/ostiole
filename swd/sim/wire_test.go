package sim_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/swd/sim"
)

type resetTarget struct {
	resets int
}

func (*resetTarget) Read(context.Context, sim.Request) (uint32, error) {
	return 0, nil
}

func (*resetTarget) Write(context.Context, sim.Request, uint32) error {
	return nil
}

func (t *resetTarget) ObserveLineReset() {
	t.resets++
}

func TestWireRecognizesProtocolEntry(t *testing.T) {
	tests := []struct {
		name string
		run  func(*swd.Conn) error
	}{
		{
			name: "line reset",
			run:  func(c *swd.Conn) error { return c.LineReset(t.Context()) },
		},
		{
			name: "JTAG to SWD",
			run:  func(c *swd.Conn) error { return c.JTAGToSWD(t.Context()) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var w sim.Wire
			if err := test.run(swd.New(&w)); err != nil {
				t.Fatalf("protocol entry: %v", err)
			}
		})
	}
}

func TestWireReportsEveryLineResetToTheTarget(t *testing.T) {
	target := &resetTarget{}
	conn := swd.New(sim.New(target))
	if err := conn.LineReset(t.Context()); err != nil {
		t.Fatalf("LineReset(): %v", err)
	}
	if target.resets != 1 {
		t.Fatalf("line-reset notifications = %d, want 1", target.resets)
	}
	if err := conn.JTAGToSWD(t.Context()); err != nil {
		t.Fatalf("JTAGToSWD(): %v", err)
	}
	if target.resets != 3 {
		t.Fatalf("line-reset notifications = %d, want 3", target.resets)
	}
}

func TestWireRejectsInvalidCalls(t *testing.T) {
	var w sim.Wire
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := w.SWDIO(ctx, nil, nil, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled SWDIO error = %v", err)
	}
	if _, err := w.SWDIO(t.Context(), nil, nil, -1); err == nil ||
		!strings.Contains(err.Error(), "negative") {
		t.Fatalf("negative-length SWDIO error = %v", err)
	}
	if _, err := w.SWDIO(t.Context(), nil, nil, 1); err == nil ||
		!strings.Contains(err.Error(), "too short") {
		t.Fatalf("short SWDIO error = %v", err)
	}
	if err := swd.New(&w).LineReset(t.Context()); err != nil {
		t.Fatalf("line reset: %v", err)
	}
	if _, err := w.SWDIO(t.Context(), []byte{0xff}, []byte{0}, 8); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("unknown-sequence SWDIO error = %v", err)
	}
}
