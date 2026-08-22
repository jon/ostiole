//go:build integration

package dap_test

import (
	"context"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
)

const (
	hardwareMemClass = uint8(0x08)
)

var hardwareAP = dap.NewAPSel(0)

func TestAccessAPOverFTDI(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	dp := openHardwareDebugPort(t, ctx)
	const hardwareAPCSW = uint8(0x00)
	var (
		savedCSW uint32
		saved    bool
	)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		if saved {
			if err := dp.WriteRawAP(cleanupCtx, hardwareAP.Address(hardwareAPCSW), savedCSW); err != nil {
				t.Errorf("restore AP0 CSW: %v", err)
			}
		}
		if err := dp.Release(cleanupCtx); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})

	info, err := dp.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	idr, err := dp.ReadAPIDR(ctx, hardwareAP)
	if err != nil {
		t.Fatal(err)
	}
	if idr.Raw == 0 || idr.Class != hardwareMemClass {
		t.Fatalf("AP0 IDR = %#08x, want a MEM-AP identity", idr.Raw)
	}
	savedCSW, err = dp.ReadRawAP(ctx, hardwareAP.Address(hardwareAPCSW))
	if err != nil {
		t.Fatal(err)
	}
	saved = true
	if err := dp.WriteRawAP(ctx, hardwareAP.Address(hardwareAPCSW), savedCSW); err != nil {
		t.Fatal(err)
	}
	gotCSW, err := dp.ReadRawAP(ctx, hardwareAP.Address(hardwareAPCSW))
	if err != nil {
		t.Fatal(err)
	}
	if gotCSW != savedCSW {
		t.Fatalf("AP0 CSW = %#08x after unchanged write, want %#08x", gotCSW, savedCSW)
	}
	t.Logf("DPIDR=%#08x AP0_IDR=%#08x AP0_CSW=%#08x", info.Raw, idr.Raw, gotCSW)
}

func TestTransactionOverFTDI(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	dp, faultWire := openHardwareDebugPortWithFaultWire(t, ctx)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		if err := dp.Release(cleanupCtx); err != nil {
			t.Errorf("release SW-DP: %v", err)
		}
	})
	if _, err := dp.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	counter := faultWire.inner.(*ackCountingWire)
	beforeCalls := counter.calls
	beforeFixedCalls := counter.fixedCalls
	beforeFixed := counter.fixed
	beforeOK := counter.counts[0b001]

	txn := dp.NewTxn()
	dpidr := txn.ReadDP(dap.DPIDR)
	idr := txn.ReadAPIDR(hardwareAP)
	csw := txn.ReadRawAP(hardwareAP.Address(0x00))
	if err := txn.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	dpidrValue, err := dpidr.Value()
	if err != nil {
		t.Fatal(err)
	}
	calls := counter.calls - beforeCalls
	fixedCalls := counter.fixedCalls - beforeFixedCalls
	frames := counter.fixed - beforeFixed
	ok := counter.counts[0b001] - beforeOK
	if calls != 2 || fixedCalls != 2 || frames != 9 || ok != 9 {
		t.Fatalf("transaction used SWDIO calls=%d fixed calls=%d frames=%d OK=%d; want 2, 2, 9, 9", calls, fixedCalls, frames, ok)
	}
	idrValue, err := idr.Value()
	if err != nil {
		t.Fatal(err)
	}
	cswValue, err := csw.Value()
	if err != nil {
		t.Fatal(err)
	}
	selector, err := hardwareAP.Value()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("transaction DPIDR=%#08x AP%d_IDR=%#08x AP%d_CSW=%#08x logical_requests=9 SWDIO_calls=2 fixed_frames=9 OK=9", dpidrValue, selector, idrValue, selector, cswValue)
}
