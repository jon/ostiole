//go:build integration

package dap_test

import (
	"context"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
)

const (
	hardwareAP       = dap.APSel(0)
	hardwareAPCSW    = dap.APReg(0x00)
	hardwareMemClass = uint32(0x08)
)

func TestAccessAPOverFTDI(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	dp := openHardwareDebugPort(t, ctx)
	var (
		savedCSW uint32
		saved    bool
	)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		if saved {
			if err := dp.WriteAP(cleanupCtx, hardwareAP, hardwareAPCSW, savedCSW); err != nil {
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
	idr, err := dp.ReadAP(ctx, hardwareAP, dap.APIDR)
	if err != nil {
		t.Fatal(err)
	}
	if idr == 0 || idr>>13&0x0f != hardwareMemClass {
		t.Fatalf("AP0 IDR = %#08x, want a MEM-AP identity", idr)
	}
	savedCSW, err = dp.ReadAP(ctx, hardwareAP, hardwareAPCSW)
	if err != nil {
		t.Fatal(err)
	}
	saved = true
	if err := dp.WriteAP(ctx, hardwareAP, hardwareAPCSW, savedCSW); err != nil {
		t.Fatal(err)
	}
	gotCSW, err := dp.ReadAP(ctx, hardwareAP, hardwareAPCSW)
	if err != nil {
		t.Fatal(err)
	}
	if gotCSW != savedCSW {
		t.Fatalf("AP0 CSW = %#08x after unchanged write, want %#08x", gotCSW, savedCSW)
	}
	t.Logf("DPIDR=%#08x AP0_IDR=%#08x AP0_CSW=%#08x", info.Raw, idr, gotCSW)
}

func TestTransactionOverFTDI(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	dp := openHardwareDebugPort(t, ctx)
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

	txn := dp.NewTxn()
	dpidr := txn.ReadDP(dap.DPIDR)
	idr := txn.ReadAP(hardwareAP, dap.APIDR)
	csw := txn.ReadAP(hardwareAP, dap.APCSW)
	if err := txn.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	dpidrValue, err := dpidr.Value()
	if err != nil {
		t.Fatal(err)
	}
	idrValue, err := idr.Value()
	if err != nil {
		t.Fatal(err)
	}
	cswValue, err := csw.Value()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("transaction DPIDR=%#08x AP%d_IDR=%#08x AP%d_CSW=%#08x", dpidrValue, hardwareAP, idrValue, hardwareAP, cswValue)
}
