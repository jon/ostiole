package jlink

import (
	"context"
	"errors"
	"fmt"
)

// ConfigureJTAG selects the advertised JTAG interface and requests a whole-kHz
// target clock no greater than maxClockHz. It does not reset or move the TAP.
// An error can leave the volatile interface or clock changed. Release any SWD
// or JTAG connection before reconfiguring its session.
func (s *Session) ConfigureJTAG(ctx context.Context, maxClockHz uint32) error {
	return s.configureTarget(ctx, maxClockHz, interfaceJTAG, "JTAG", 8)
}

// JTAGIO clocks packed TMS and TDI streams and returns packed TDO samples,
// earliest bit first. It requires successful JTAG configuration, never switches
// interfaces, and does not modify or retain the buffers. Zero clocks perform
// no traffic. A nonzero scan status requires reconfiguration; an ambiguous
// transfer poisons the session. Scans are never replayed.
func (s *Session) JTAGIO(ctx context.Context, tms, tdi []byte, bits int) ([]byte, error) {
	if bits < 0 || bits > defaultTransferBits || len(tms)*8 < bits || len(tdi)*8 < bits {
		return nil, fmt.Errorf("jlink: invalid %d-bit JTAG stream", bits)
	}
	if err := s.transportReady(ctx); err != nil {
		return nil, err
	}
	if !s.configured || s.info.SelectedInterface != interfaceJTAG {
		return nil, errors.New("jlink: JTAG is not configured")
	}
	if bits > s.transferBits {
		return nil, fmt.Errorf("jlink: %d-bit JTAG stream exceeds %d-bit transfer limit", bits, s.transferBits)
	}
	if bits == 0 {
		return []byte{}, nil
	}
	size := (bits + 7) / 8
	control, drive := append([]byte(nil), tms[:size]...), append([]byte(nil), tdi[:size]...)
	maskUnusedBits(control, bits)
	maskUnusedBits(drive, bits)
	return s.scan(ctx, control, drive, bits, "JTAG")
}
