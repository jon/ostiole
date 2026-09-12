package dap_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/jon/ostiole/armdebug"
	"github.com/jon/ostiole/discover"
	"github.com/jon/ostiole/probe"
)

type armAttachment struct{}

func (armAttachment) Info() discover.AttachmentInfo {
	return discover.AttachmentInfo{Key: "one"}
}

var armFixtureID atomic.Uint64

func TestArmJTAGOpenSelectsAndActivatesOnce(t *testing.T) {
	_, b, cfg := armJTAGFixture(t, 8, 1)
	id := discover.ProviderID(fmt.Sprintf("arm-jtag-%d", armFixtureID.Add(1)))
	enumerations, opens := 0, 0
	if err := discover.RegisterTransport(discover.NewTransportProvider(id, func(context.Context) ([]discover.Attachment, error) {
		enumerations++
		return []discover.Attachment{armAttachment{}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := discover.RegisterProbe(discover.NewProbeProvider(id, id, func(context.Context, discover.Transport) ([]discover.Candidate, error) {
		return []discover.Candidate{discover.NewCandidate(probe.Info{Serial: "selected"}, "one", func(context.Context) (*probe.Probe, error) {
			opens++
			return probe.New(probe.Info{Serial: "selected"}, b), nil
		})}, nil
	})); err != nil {
		t.Fatal(err)
	}
	c, err := armdebug.Open(t.Context(), discover.Selection{Provider: id, Serial: "selected"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if enumerations != 1 || opens != 1 || b.activations != 1 {
		t.Fatalf("open counts: %d/%d/%d", enumerations, opens, b.activations)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
