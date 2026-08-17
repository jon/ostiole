package dap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestAccessSelectedAPRegisters(t *testing.T) {
	target := sim.New(0x2ba01477)
	target.AddAP(0, 0x04770031)
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dp.Release(context.Background()); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})

	idr, err := dp.ReadAPIDR(t.Context(), apSel(0))
	if err != nil {
		t.Fatal(err)
	}
	if idr.Raw != 0x04770031 {
		t.Fatalf("AP0 IDR = %#08x, want 0x04770031", idr.Raw)
	}
	absent, err := dp.ReadAPIDR(t.Context(), apSel(1))
	if err != nil {
		t.Fatal(err)
	}
	if absent.Raw != 0 {
		t.Fatalf("absent AP1 IDR = %#08x, want 0", absent.Raw)
	}

	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 0x12345678); err != nil {
		t.Fatal(err)
	}
	value, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0))
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x12345678 {
		t.Fatalf("AP0 register 0 = %#08x, want 0x12345678", value)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x94), 0xa5a55a5a); err != nil {
		t.Fatal(err)
	}
	value, err = dp.ReadRawAP(t.Context(), apSel(0).Address(0x94))
	if err != nil {
		t.Fatal(err)
	}
	if value != 0xa5a55a5a {
		t.Fatalf("AP0 address 0x94 = %#08x, want 0xa5a55a5a", value)
	}
}

func TestAPAccessRequiresConnectedDebugPort(t *testing.T) {
	dp := enteredDP(t, sim.New(0x2ba01477))
	if _, err := dp.ReadAPIDR(t.Context(), apSel(0)); err == nil {
		t.Fatal("ReadAPIDR() succeeded before Connect()")
	}
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 1); err == nil {
		t.Fatal("WriteRawAP() succeeded after Release()")
	}
}

func TestAPWriteWaitsForRDBUFF(t *testing.T) {
	target := &barrierTarget{Target: sim.New(0x2ba01477)}
	target.AddAP(0, 0x04770031)
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dp.Release(context.Background()); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})
	target.failBarrier = true
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0), 1); !errors.Is(err, errBarrier) {
		t.Fatalf("WriteRawAP() error = %v, want %v", err, errBarrier)
	}
	if _, err := dp.ReadDP(t.Context(), dap.DPIDR); err == nil {
		t.Fatal("ReadDP() succeeded after the AP completion transfer failed")
	}
}

func TestDecodeAPIDR(t *testing.T) {
	info := dap.DecodeAPIDR(0x8477f123)
	if info.Raw != 0x8477f123 || info.Revision != 8 || info.Designer != 0x23b || info.Class != 0xf || info.Variant != 2 || info.Type != 3 {
		t.Fatalf("DecodeAPIDR() = %+v", info)
	}
}

func TestRawAPAccessRejectsInvalidAddressesBeforeTraffic(t *testing.T) {
	target := &countingTarget{Target: sim.New(0x2ba01477)}
	target.AddAP(0, 0x04770031)
	dp := enteredDP(t, target)
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dp.Release(context.Background()) })

	before := target.requests
	var zeroSel dap.APSel
	var zeroAddr dap.APAddress
	if _, err := dp.ReadAPIDR(t.Context(), zeroSel); err == nil {
		t.Fatal("ReadAPIDR() accepted a zero APSel")
	}
	if _, err := dp.ReadRawAP(t.Context(), zeroAddr); err == nil {
		t.Fatal("ReadRawAP() accepted a zero APAddress")
	}
	if err := dp.WriteRawAP(t.Context(), zeroAddr, 1); err == nil {
		t.Fatal("WriteRawAP() accepted a zero APAddress")
	}
	if _, err := dp.ReadRawAP(t.Context(), zeroSel.Address(0)); err == nil {
		t.Fatal("ReadRawAP() accepted a zero APSel")
	}
	if err := dp.WriteRawAP(t.Context(), zeroSel.Address(0), 1); err == nil {
		t.Fatal("WriteRawAP() accepted a zero APSel")
	}
	if _, err := dp.ReadRawAP(t.Context(), apSel(0).Address(0x02)); err == nil {
		t.Fatal("ReadRawAP() accepted unaligned address 0x02")
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0x02), 1); err == nil {
		t.Fatal("WriteRawAP() accepted unaligned address 0x02")
	}
	if err := dp.WriteRawAP(t.Context(), apSel(0).Address(0xfc), 1); err == nil {
		t.Fatal("WriteRawAP() accepted APIDR")
	}
	if target.requests != before {
		t.Fatalf("invalid accesses sent %d requests", target.requests-before)
	}
}

func TestAPSelValue(t *testing.T) {
	selector, err := dap.NewAPSel(255).Value()
	if err != nil {
		t.Fatal(err)
	}
	if selector != 255 {
		t.Fatalf("APSel.Value() = %d, want 255", selector)
	}
	var zero dap.APSel
	if _, err := zero.Value(); err == nil {
		t.Fatal("zero APSel.Value() succeeded")
	}
}

var errBarrier = errors.New("posted access failed")

type barrierTarget struct {
	*sim.Target
	failBarrier bool
	postedRead  bool
	postedWrite bool
}

type countingTarget struct {
	*sim.Target
	requests int
}

func (t *countingTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	t.requests++
	return t.Target.Read(ctx, req)
}

func (t *countingTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	t.requests++
	return t.Target.Write(ctx, req, value)
}

func (t *barrierTarget) Read(ctx context.Context, req swdsim.Request) (uint32, error) {
	if !req.AP && req.Addr == 0x0c && (t.postedRead || t.postedWrite) {
		t.postedRead = false
		t.postedWrite = false
		if t.failBarrier {
			return 0, errBarrier
		}
	}
	if req.AP {
		t.postedRead = true
	}
	return t.Target.Read(ctx, req)
}

func (t *barrierTarget) Write(ctx context.Context, req swdsim.Request, value uint32) error {
	if req.AP {
		t.postedWrite = true
	}
	return t.Target.Write(ctx, req, value)
}

var _ swdsim.Target = (*barrierTarget)(nil)
