package dap_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/dap"
)

func TestRegisterDPWrappedWAITCancellationDropsWAIT(t *testing.T) {
	model := newRegisterModel(t)
	dp := dap.NewDebugPort(dap.RegisterDP(model))
	if _, err := dp.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	releaseErrorPort(t, dp)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	model.failFor = dpRead(12)
	model.failStatus = dap.RegisterRejected
	model.failCause = fmt.Errorf("response: %w", dap.ErrWait)
	model.onCompletion = cancel
	_, err := dp.ReadDP(ctx, dap.RDBUFF)
	if !errors.Is(err, context.Canceled) || errors.Is(err, dap.ErrWait) {
		t.Fatalf("canceled wrapped WAIT: %v", err)
	}
}
