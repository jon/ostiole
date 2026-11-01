package armdebug_test

import (
	"errors"
	"testing"

	"github.com/jon/ostiole/armdebug"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestConnectionRestorationQueryRetainsLiveFailureAndSendsNoTraffic(t *testing.T) {
	var absent *armdebug.Conn
	for _, empty := range []*armdebug.Conn{absent, {}} {
		if empty.RestorationPossible() {
			t.Fatal("inactive connection reports restoration available")
		}
	}
	c, b := openTerminalBench(t)
	calls := b.transfers
	if !c.RestorationPossible() || b.transfers != calls || b.closes != 0 {
		t.Fatal("live restoration query sent traffic or lost availability")
	}
	failure := errors.New("live AP restoration failure")
	b.target.beforeWrite = func(req swdsim.Request, _, _ uint32) error {
		if req.AP {
			return failure
		}
		return nil
	}
	if err := c.Close(); !errors.Is(err, failure) || b.closes != 0 {
		t.Fatalf("live cleanup=%v", err)
	}
	calls = b.transfers
	if !c.RestorationPossible() || b.transfers != calls {
		t.Fatal("live cleanup failure reported terminal loss")
	}
	b.possible = false
	if c.RestorationPossible() || b.transfers != calls || b.closes != 0 {
		t.Fatal("terminal state query sent traffic or claimed availability")
	}
	if err := c.Close(); !errors.Is(err, armdebug.ErrRestorationAbandoned) || b.closes != 1 {
		t.Fatalf("terminal cleanup=%v", err)
	}
	if c.RestorationPossible() || b.transfers != calls {
		t.Fatal("released connection reports restoration available or sends traffic")
	}
}
