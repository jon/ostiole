package dap_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jon/ostiole/dap"
)

func TestRegisterFailedEntryReleasesOnlyAcquiredState(t *testing.T) {
	for _, acquired := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsent", true: "acquired"}[acquired], func(t *testing.T) {
			model := newRegisterModel(t)
			model.target.SetOverrunDetect(true)
			transport := &registerLifecycle{registerModel: model}
			cause := errors.New("entry failed")
			transport.enter = func(ctx context.Context) (uint32, error) {
				if acquired {
					if _, err := model.Connect(ctx); err != nil {
						return 0, err
					}
				}
				return 0, cause
			}
			attempts := 0
			transport.leave = func(ctx context.Context) error {
				attempts++
				deadline, ok := ctx.Deadline()
				if ctx.Err() != nil || !ok || time.Until(deadline) > time.Second {
					t.Errorf("entry cleanup context=%v deadline=%v", ctx.Err(), deadline)
				}
				return model.Release(ctx)
			}
			dp := dap.NewDebugPort(dap.RegisterDP(transport))
			if _, err := dp.Connect(t.Context()); !errors.Is(err, cause) {
				t.Fatalf("entry=%v", err)
			}
			want := 0
			if acquired {
				want = 1
			}
			if attempts != want || dp.CleanupPending() || model.active || !model.target.OverrunDetectEnabled() {
				t.Fatalf("cleanup attempts=%d pending=%v active=%v overrun=%v", attempts, dp.CleanupPending(), model.active, model.target.OverrunDetectEnabled())
			}
		})
	}
}

func TestRegisterFinalReleaseErrorIsReportedOnce(t *testing.T) {
	model := newRegisterModel(t)
	transport := &registerLifecycle{registerModel: model}
	dp := dap.NewDebugPort(dap.RegisterDP(transport))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("final completion report")
	transport.leave = func(ctx context.Context) error {
		return errors.Join(model.Release(ctx), cause, context.DeadlineExceeded)
	}
	err := dp.Release(t.Context())
	if !errors.Is(err, cause) || !errors.Is(err, context.DeadlineExceeded) || dp.CleanupPending() {
		t.Fatalf("final release=%v pending=%v", err, dp.CleanupPending())
	}
	before := len(model.requests)
	connects, releases := model.connects, model.releases
	if err := dp.Release(t.Context()); err != nil || len(model.requests) != before || model.connects != connects || model.releases != releases {
		t.Fatalf("idle release=%v traffic=%d/%d connects=%d releases=%d", err, len(model.requests), before, model.connects, model.releases)
	}
}

func TestRegisterFailedEntryKeepsCleanupFailureForRetry(t *testing.T) {
	model := newRegisterModel(t)
	model.target.SetOverrunDetect(true)
	transport := &registerLifecycle{registerModel: model}
	primary, cleanup := errors.New("entry completion failed"), errors.New("restore failed")
	transport.enter = func(ctx context.Context) (uint32, error) {
		if _, err := model.Connect(ctx); err != nil {
			return 0, err
		}
		return 0, primary
	}
	transport.leave = func(context.Context) error { return cleanup }
	dp := dap.NewDebugPort(dap.RegisterDP(transport))
	if _, err := dp.Connect(t.Context()); !errors.Is(err, primary) || !errors.Is(err, cleanup) || !dp.CleanupPending() {
		t.Fatalf("entry=%v pending=%v", err, dp.CleanupPending())
	}
	before := len(model.requests)
	if _, err := dp.ReadDP(t.Context(), dap.CTRLSTAT); err == nil || len(model.requests) != before {
		t.Fatal("ordinary traffic during failed entry cleanup")
	}
	transport.enter = nil
	transport.leave = nil
	if err := dp.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if dp.CleanupPending() || model.active || !model.target.OverrunDetectEnabled() || model.connects != 2 {
		t.Fatalf("retry pending=%v active=%v overrun=%v connects=%d", dp.CleanupPending(), model.active, model.target.OverrunDetectEnabled(), model.connects)
	}
}
