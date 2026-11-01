package ftdi

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestMaximumJTAGResponseIncludesCompletionEcho(t *testing.T) {
	c, raw, _ := completionChannel(t)
	samples := bytes.Repeat([]byte{0x80}, maxJTAGTransferBits)
	response := append(samples, 0xfa, 0xab)
	raw.mu.Lock()
	for len(response) > 0 {
		n := min(510, len(response))
		raw.readData = append(raw.readData, append([]byte{1, 0x60}, response[:n]...))
		response = response[n:]
	}
	raw.mu.Unlock()
	tms := bytes.Repeat([]byte{0xff}, maxJTAGTransferBits/8)
	tdi := bytes.Repeat([]byte{0x55}, len(tms))
	got, err := c.JTAGIO(t.Context(), tms, tdi, maxJTAGTransferBits)
	if err != nil || !bytes.Equal(got, tms) {
		t.Fatalf("maximum JTAG response: %d bytes, %v", len(got), err)
	}
}

func TestWireWaitsForMissingCompletionMarker(t *testing.T) {
	for _, protocol := range []string{"SWD", "JTAG"} {
		t.Run(protocol, func(t *testing.T) {
			c, raw, _ := completionChannel(t)
			raw.mu.Lock()
			raw.readData = [][]byte{{1, 0x60, 0x80}}
			raw.mu.Unlock()
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()
			var err error
			if protocol == "SWD" {
				_, err = c.SWDIO(ctx, []byte{0xf0}, []byte{0xa0}, 8)
			} else {
				_, err = c.JTAGIO(ctx, []byte{1}, []byte{0}, 1)
			}
			if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrChannelPoisoned) {
				t.Fatalf("samples returned before marker: %v", err)
			}
		})
	}
}
