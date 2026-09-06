package armdebug_test

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestMemAPCloseResumesAtFailedAP(t *testing.T) {
	c, b := connectMemoryBench(t)
	for _, ap := range []uint8{0, 1} {
		m, err := c.OpenMemAP(t.Context(), dap.NewAPSel(ap))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.ReadWord(t.Context(), 0x20000000); err != nil {
			t.Fatal(err)
		}
	}
	want := errors.New("AP0 restoration unavailable")
	fail := true
	var writes []uint32
	b.target.beforeWrite = func(req swdsim.Request, selected, _ uint32) error {
		if req.AP {
			ap := selected >> 24
			writes = append(writes, ap)
			if ap == 0 && fail {
				return want
			}
		}
		return nil
	}
	if err := c.Close(); !errors.Is(err, want) || b.closes != 0 {
		t.Fatalf("release failure: %v", err)
	}
	if len(writes) < 2 || writes[0] != 1 || writes[len(writes)-1] != 0 {
		t.Fatalf("not reverse acquisition order: %v", writes)
	}
	fail = false
	writes = nil
	if err := c.Close(); err != nil || b.closes != 1 {
		t.Fatalf("release retry: %v", err)
	}
	for _, ap := range writes {
		if ap != 0 {
			t.Fatalf("repeated a completed AP release: %v", writes)
		}
	}
}
