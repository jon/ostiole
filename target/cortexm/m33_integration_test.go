//go:build integration

package cortexm_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/jon/ostiole/dap"
)

func TestHILRP2350Control(t *testing.T) {
	if os.Getenv("OSTIOLE_RP2350_HIL_CONTROL") != "1" {
		t.Skip("OSTIOLE_RP2350_HIL_CONTROL is not 1")
	}
	program := os.Getenv("OSTIOLE_RP2350_HIL_PROGRAM")
	counter, err := strconv.ParseUint(os.Getenv("OSTIOLE_RP2350_HIL_COUNTER"), 0, 32)
	if err != nil || counter%4 != 0 || program == "" {
		t.Fatal("require a known bench PROGRAM and aligned RAM COUNTER address")
	}
	ap, err := dap.APAt(0x2000)
	if err != nil {
		t.Fatal(err)
	}
	bench := controlBench{
		name:     "RP2350 core 0 J-Link 1 MHz AP 0x2000",
		provider: "jlink", serial: "000802011345", ap: ap, cpuid: 0x411fd210,
	}
	for range 2 {
		if !t.Run("session", func(t *testing.T) { controlHIL(t, uint32(counter), program, bench) }) {
			return
		}
	}
}
