package sim_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jon/ostiole/dap"
	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/target/cortexm"
	"github.com/jon/ostiole/target/cortexm/sim"
)

func TestComposedRegisterTransfersFailOutsideModel(t *testing.T) {
	for _, profile := range []sim.Profile{sim.M0, sim.M33} {
		for _, writing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/write=%v", profile, writing), func(t *testing.T) {
				checkComposedRegisterFailure(t, profile, writing)
			})
		}
	}
}

func checkComposedRegisterFailure(t *testing.T, profile sim.Profile, writing bool) {
	t.Helper()
	b := compose(t, profile, false)
	if _, err := b.group.Halt(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	var err error
	if writing {
		err = b.group.WriteRegister(t.Context(), 1, cortexm.R0, 0x12345678)
	} else {
		var value uint32
		value, err = b.group.ReadRegister(t.Context(), 1, cortexm.R0)
		if value != 0 {
			t.Fatalf("failed register read returned %#x", value)
		}
	}
	if !errors.Is(err, dap.ErrFault) {
		t.Fatalf("unsupported register access: %v", err)
	}
	if _, err := b.group.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := b.owner.Close(); err != nil {
		t.Fatal(err)
	}
	if b.probe.closes != 1 {
		t.Fatal("lower owner not closed exactly once")
	}
}

func TestComposedDebugWindowDoesNotFallThroughToRAM(t *testing.T) {
	for _, offset := range []uint32{4, 8, 12} {
		for _, writing := range []bool{false, true} {
			t.Run(fmt.Sprintf("offset%d/write=%v", offset, writing), func(t *testing.T) {
				b := compose(t, sim.M33, false)
				before := b.cores[0].Snapshot()
				addr := uint32(dhcsr) + offset
				var err error
				if writing {
					err = b.memories[0].WriteWord(t.Context(), addr, 0x12345678)
				} else {
					_, err = b.memories[0].ReadWord(t.Context(), addr)
				}
				if !errors.Is(err, dap.ErrFault) || b.cores[0].Snapshot() != before {
					t.Fatalf("unsupported debug word: %v, snapshot %+v want %+v", err, b.cores[0].Snapshot(), before)
				}
				if _, err := b.group.Release(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := b.owner.Close(); err != nil {
					t.Fatal(err)
				}
				if b.probe.closes != 1 {
					t.Fatal("lower owner not closed exactly once")
				}
			})
		}
	}
}

func TestComposedStepFailureRetainsUncertainLaunch(t *testing.T) {
	for _, profile := range []sim.Profile{sim.M0, sim.M33} {
		t.Run(fmt.Sprint(profile), func(t *testing.T) {
			b := compose(t, profile, false)
			if _, err := b.group.Halt(t.Context(), 1); err != nil {
				t.Fatal(err)
			}
			before := b.cores[0].Snapshot()
			err := b.group.Step(t.Context(), 1)
			if !errors.Is(err, dapsim.ErrDeviceFailure) || !strings.Contains(err.Error(), sim.ErrUnsupported.Error()) {
				t.Fatalf("unsupported step: %v", err)
			}
			if b.cores[0].Snapshot() != before {
				t.Fatal("unsupported step changed modeled state")
			}
			if _, err := b.group.Release(t.Context()); err == nil || !b.group.Results()[0].CleanupPending {
				t.Fatal("uncertain launch lost cleanup obligation")
			}
			if b.probe.closes != 0 {
				t.Fatal("failed group cleanup closed the borrowed owner")
			}
		})
	}
}
