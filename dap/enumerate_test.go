package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

type enumerationFaultTarget struct {
	*waitTarget
	faultSel uint8
	armed    bool
}

func (t *enumerationFaultTarget) Acknowledge(ctx context.Context, req swdsim.Request) error {
	if t.armed && req.AP && req.Read && req.Addr == 0x0c && t.currentAP() == t.faultSel {
		t.armed = false
		return swd.ErrFault
	}
	return t.waitTarget.Acknowledge(ctx, req)
}

func (t *enumerationFaultTarget) currentAP() uint8 {
	if len(t.selectValues) == 0 {
		return 0
	}
	return uint8(t.selectValues[len(t.selectValues)-1] >> 24)
}

func TestEnumerateAPsFindsSparsePorts(t *testing.T) {
	target := newWaitTarget()
	addAP(t, target, 0, 0x24770011)
	addAP(t, target, 17, 0x14770002)
	addAP(t, target, 255, 0x04770003)
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	ports, err := dp.EnumerateAPs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 3 || ports[0].Selector != apSel(0) || ports[1].Selector != apSel(17) || ports[2].Selector != apSel(255) {
		t.Fatalf("EnumerateAPs() = %+v, want selectors [0 17 255]", ports)
	}
	if ports[0].Identity != dap.DecodeAPIDR(0x24770011) || ports[1].Identity != dap.DecodeAPIDR(0x14770002) || ports[2].Identity != dap.DecodeAPIDR(0x04770003) {
		t.Fatalf("EnumerateAPs() identities = %+v, want raw IDRs [0x24770011 0x14770002 0x04770003]", ports)
	}
	for _, req := range target.requests {
		if req.AP && req.Addr != 0x0c {
			t.Fatalf("EnumerateAPs() read AP offset %#x, want IDR offset 0x0c", req.Addr)
		}
	}
}

func TestEnumerateAPsReturnsConfirmedPrefixOnFAULT(t *testing.T) {
	base := newWaitTarget()
	for sel := range 40 {
		addAP(t, base, uint8(sel), 0x24770011+uint32(sel))
	}
	target := &enumerationFaultTarget{waitTarget: base, faultSel: 33, armed: true}
	dp := newDebugPort(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	ports, err := dp.EnumerateAPs(t.Context())
	if !errors.Is(err, swd.ErrFault) {
		t.Fatalf("EnumerateAPs() error = %v, want FAULT", err)
	}
	if len(ports) != 33 || ports[len(ports)-1].Selector != apSel(32) {
		t.Fatalf("EnumerateAPs() confirmed ports = %+v, want selectors 0 through 32", ports)
	}
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err != nil {
		t.Fatalf("ReadAPIDR() after FAULT cleanup: %v", err)
	}
}

func TestEnumerateAPsRequiresConnection(t *testing.T) {
	target := newWaitTarget()
	dp := newDebugPort(t, target)
	before := len(target.requests)
	if _, err := dp.EnumerateAPs(t.Context()); err == nil {
		t.Fatal("EnumerateAPs() succeeded without Connect")
	}
	if len(target.requests) != before {
		t.Fatalf("requests after blocked enumeration = %d, want %d", len(target.requests), before)
	}
}
