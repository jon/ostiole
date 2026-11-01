package dap_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
)

func TestRegisterAPSelectionCannotConfirmUnsentAccess(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			model := newRegisterModel(t)
			dp := dap.NewDebugPort(dap.RegisterDP(model))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			releaseErrorPort(t, dp)
			model.failFor = dpWrite(8)
			model.afterWrite = true
			model.failStatus = dap.RegisterCompleted
			model.failCause = context.Canceled
			before := len(model.requests)
			txn := dp.NewTxn()
			var result func() error
			if write {
				w := txn.WriteRawAP(dap.NewAPSel(1).Address(4), 0x88)
				result = w.Err
			} else {
				r := txn.ReadAPIDR(dap.NewAPSel(1))
				result = func() error { _, err := r.Value(); return err }
			}
			if err := txn.Commit(t.Context()); !errors.Is(err, context.Canceled) {
				t.Fatalf("selection error=%v", err)
			}
			if err := result(); !errors.Is(err, dap.ErrNotExecuted) {
				t.Fatalf("unsent AP result=%v", err)
			}
			for _, req := range model.requests[before:] {
				if req.AP {
					t.Fatalf("AP traffic after prerequisite error: %v", req)
				}
			}
		})
	}
}

func TestRegisterAcceptedDAPABORTFaultInvalidatesClients(t *testing.T) {
	for _, status := range []dap.RegisterStatus{dap.RegisterRejected, dap.RegisterCompleted, dap.RegisterIndeterminate} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			model := newRegisterModel(t)
			dp := dap.NewDebugPort(dap.RegisterDP(model))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			releaseErrorPort(t, dp)
			mem, err := dap.OpenMemAP(t.Context(), dp, dap.NewAPSel(1))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := mem.Release(ctx); err != nil {
					t.Error(err)
				}
			})
			model.failFor = dpWrite(0)
			model.failStatus = status
			model.failCause = dap.ErrFault
			model.afterWrite = status != dap.RegisterRejected
			err = dp.WriteDP(t.Context(), dap.ABORT, 1)
			if !errors.Is(err, dap.ErrFault) || status != dap.RegisterRejected && !errors.Is(err, dap.ErrIndeterminate) {
				t.Fatalf("abort outcome=%v", err)
			}
			before := len(model.requests)
			_, err = mem.ReadWord(t.Context(), 0x100)
			if status == dap.RegisterRejected {
				if err != nil {
					t.Fatalf("rejected abort invalidated client: %v", err)
				}
			} else if err == nil || len(model.requests) != before {
				t.Fatalf("accepted abort left stale client usable: %v", err)
			}
		})
	}
}

func TestRegisterAPTxnPreflightKeepsDPPrefixUnsent(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	before := len(model.requests)
	txn := dp.NewTxn()
	first := txn.WriteDP(dap.SELECT, 1)
	invalid := txn.ReadRawAP(dap.APAddress{})
	if err := txn.Commit(t.Context()); err == nil || len(model.requests) != before {
		t.Fatalf("invalid queue: %v traffic=%d/%d", err, len(model.requests), before)
	}
	if !errors.Is(first.Err(), dap.ErrNotExecuted) {
		t.Fatalf("prefix=%v", first.Err())
	}
	if _, err := invalid.Value(); err == nil {
		t.Fatalf("invalid result=%v", err)
	}
}

func TestRegisterRawAPAccessRetainsClientInvalidation(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			model := newRegisterModel(t)
			dp := dap.NewDebugPort(dap.RegisterDP(model))
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			releaseErrorPort(t, dp)
			sel := dap.NewAPSel(1)
			mem, err := dap.OpenMemAP(t.Context(), dp, sel)
			if err != nil {
				t.Fatal(err)
			}
			if write {
				err = dp.WriteRawAP(t.Context(), sel.Address(4), 0x100)
			} else {
				_, err = dp.ReadRawAP(t.Context(), sel.Address(0xfc))
			}
			if err != nil {
				t.Fatal(err)
			}
			before := len(model.requests)
			if _, err := mem.ReadWord(t.Context(), 0x100); err == nil || len(model.requests) != before {
				t.Fatalf("raw access left client usable: %v", err)
			}
			if err := mem.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
