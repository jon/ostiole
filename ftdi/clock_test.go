package ftdi

import "testing"

func TestClockDivisorNeverExceedsTheRequestedRate(t *testing.T) {
	for _, requested := range []uint32{
		minClockHz,
		400_000,
		1_000_000,
		30_000_000,
		30_000_001,
	} {
		divisor, err := clockDivisor(requested)
		if err != nil {
			t.Fatalf("clockDivisor(%d) error = %v", requested, err)
		}
		denominator := uint64(2) * uint64(divisor+1)
		if uint64(baseClockHz) > uint64(requested)*denominator {
			t.Fatalf("clockDivisor(%d) configures more than requested", requested)
		}
	}
}

func TestClockDivisorRejectsUnattainablySlowRates(t *testing.T) {
	for _, requested := range []uint32{0, minClockHz - 1} {
		if _, err := clockDivisor(requested); err == nil {
			t.Fatalf("clockDivisor(%d) succeeded", requested)
		}
	}
}
