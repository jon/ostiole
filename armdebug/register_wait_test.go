package armdebug_test

import (
	"context"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/probe"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type registerWaitTarget struct {
	*observedTarget
	armed    bool
	attempts int
}

func (w *registerWaitTarget) Acknowledge(ctx context.Context, r swdsim.Request) error {
	if r.AP && r.Read && r.Addr == 12 {
		w.attempts++
		if w.armed {
			w.armed = false
			return swd.ErrWait
		}
	}
	return w.observedTarget.Acknowledge(ctx, r)
}
func TestRegisterOwnerRetriesRejectedWAIT(t *testing.T) {
	b := &registerBench{bench: newBench()}
	if err := b.target.AddMEMAP(dap.NewAPSel(1), 0x24770011, nil); err != nil {
		t.Fatal(err)
	}
	w := &registerWaitTarget{observedTarget: b.target}
	b.Wire = swdsim.New(w)
	c, err := armdebug.Connect(t.Context(), probe.New(probe.Info{}, b), config())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()
	w.armed = true
	id, err := c.Port().ReadAPIDR(t.Context(), dap.NewAPSel(1))
	if err != nil || id.Raw != 0x24770011 || w.attempts != 2 {
		t.Fatalf("rejected WAIT did not retry: id=%#x err=%v attempts=%d", id.Raw, err, w.attempts)
	}
}
