package dap

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jon/ostiole/swd"
)

type nativeWAITFailure struct{ cause error }

func (e *nativeWAITFailure) Error() string { return "response completion: " + e.cause.Error() }
func (e *nativeWAITFailure) Unwrap() error { return e.cause }

func TestCanceledWAITCauseKeepsWrappedFailures(t *testing.T) {
	for _, wait := range []error{ErrWait, swd.ErrWait} {
		for _, canceled := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(fmt.Sprint(wait, canceled), func(t *testing.T) {
				native := &nativeWAITFailure{cause: errors.Join(wait, canceled)}
				cause := fmt.Errorf("transport: %w", native)
				got := canceledWAITCause(context.Background(), cause)
				var detail *nativeWAITFailure
				if !errors.Is(got, canceled) || !errors.Is(got, native) || !errors.As(got, &detail) || detail != native {
					t.Fatalf("lost completion cause or type: %v", got)
				}
				if errors.Is(got, ErrWait) || errors.Is(got, swd.ErrWait) {
					t.Fatalf("canceled completion still classified WAIT: %v", got)
				}
				if got.Error() != cause.Error() {
					t.Fatalf("lost diagnostic: %v", got)
				}
			})
		}
	}
}

func TestCallerCancellationReplacesWrappedSoleWAIT(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, cause := range []error{fmt.Errorf("response: %w", ErrWait), fmt.Errorf("response: %w", swd.ErrWait)} {
		err := canceledWAITCause(ctx, cause)
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrWait) || errors.Is(err, swd.ErrWait) {
			t.Fatalf("canceled WAIT: %v", err)
		}
	}
}
