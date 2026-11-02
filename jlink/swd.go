package jlink

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	commandClockDescribe = 0xc0
	commandScanV3        = 0xcf
	commandClockSet      = 0x05
	interfaceSWD         = 1
	interfaceJTAG        = 0
	capabilityClock      = 9
	defaultTransferBits  = 504
	minimumSWDFrameBits  = 136
)

// ScanError reports a complete scan response with a nonzero probe status.
// Status 6 means the probe had insufficient workspace.
type ScanError struct {
	Status uint8
}

func (e *ScanError) Error() string {
	if e.Status == 6 {
		return "jlink: insufficient workspace for scan"
	}
	return fmt.Sprintf("jlink: scan failed with status %#02x", e.Status)
}

// ConfigureSWD selects the advertised SWD interface and requests a whole-kHz
// target clock no greater than maxClockHz. Reconfiguration resets adapter
// sample state. An error can leave the probe's volatile target interface or
// clock changed. Release any SWD or JTAG connection before reconfiguring.
func (s *Session) ConfigureSWD(ctx context.Context, maxClockHz uint32) error {
	return s.configureTarget(ctx, maxClockHz, interfaceSWD, "SWD", minimumSWDFrameBits)
}

func (s *Session) configureTarget(ctx context.Context, maxClockHz uint32, target uint8, name string, minimumBits int) error {
	if maxClockHz < 1_000 {
		return fmt.Errorf("jlink: %s clock ceiling must be at least 1 kHz", name)
	}
	if err := s.transportReady(ctx); err != nil {
		return err
	}
	if !s.info.SelectedInterfaceKnown || s.info.AvailableInterfaces&(1<<target) == 0 {
		return fmt.Errorf("jlink: probe does not advertise configurable %s", name)
	}
	limit, err := s.scanTransferLimit(minimumBits)
	if err != nil {
		return err
	}
	s.configured = false
	s.scanFailed = false
	response, err := s.exchange(ctx, []byte{commandInterface, target}, 4)
	if err != nil {
		return fmt.Errorf("jlink: select %s interface: %w", name, err)
	}
	if previous := binary.LittleEndian.Uint32(response); previous >= 32 {
		return fmt.Errorf("jlink: invalid previous target interface %d", previous)
	}
	s.info.SelectedInterface = target
	clockHz, err := s.targetClock(ctx, maxClockHz)
	if err != nil {
		return err
	}
	request := []byte{commandClockSet, byte(clockHz / 1_000), byte(clockHz / 1_000 >> 8)}
	if err := s.writeExact(ctx, request); err != nil {
		return fmt.Errorf("jlink: set %s clock: %w", name, err)
	}
	s.clockHz, s.transferBits = clockHz, limit
	s.delayInput = target == interfaceSWD
	s.inputCarry = false
	s.configured = true
	return nil
}

func (s *Session) targetClock(ctx context.Context, ceiling uint32) (uint32, error) {
	rateKHz := ceiling / 1_000
	if rateKHz > 0xfffe {
		rateKHz = 0xfffe
	}
	if s.info.Capabilities.Has(capabilityClock) {
		response, err := s.exchange(ctx, []byte{commandClockDescribe}, 6)
		if err != nil {
			return 0, fmt.Errorf("jlink: describe target clock: %w", err)
		}
		base := binary.LittleEndian.Uint32(response)
		minimumDivisor := uint32(binary.LittleEndian.Uint16(response[4:]))
		if base == 0 || minimumDivisor == 0 {
			return 0, errors.New("jlink: invalid target clock description")
		}
		maximumKHz := base / minimumDivisor / 1_000
		if rateKHz > maximumKHz {
			rateKHz = maximumKHz
		}
	}
	if rateKHz == 0 {
		return 0, errors.New("jlink: probe cannot provide a 1 kHz target clock")
	}
	return rateKHz * 1_000, nil
}

func (s *Session) scanTransferLimit(minimumBits int) (int, error) {
	limitBytes := uint32(defaultTransferBits / 8)
	if s.info.WorkspaceKnown {
		workspaceBytes := uint32(0)
		if s.info.Workspace > 4 {
			workspaceBytes = (s.info.Workspace - 4) / 2
		}
		if workspaceBytes < limitBytes {
			limitBytes = workspaceBytes
		}
	}
	limit := int(limitBytes) * 8
	if limit < minimumBits {
		return 0, fmt.Errorf("jlink: probe transfer limit is only %d bits", limit)
	}
	return limit, nil
}

// ClockHz reports the target clock requested by the most recent successful
// SWD or JTAG configuration, or zero while unconfigured. The probe does not
// acknowledge the clock request.
func (s *Session) ClockHz() uint32 {
	if s == nil || !s.configured {
		return 0
	}
	return s.clockHz
}

// MaxTransferBits reports the scan limit without I/O. A probe-owned session
// retains its limit after a complete status error so cleanup can reconfigure it.
// Other unconfigured sessions report zero.
func (s *Session) MaxTransferBits() int {
	if s == nil || (!s.configured && !s.recoverableScan()) {
		return 0
	}
	return s.transferBits
}

// SWDIO executes one direction-explicit SWD bit stream through scan v3. Bits
// are packed least-significant first. A set direction bit drives the matching
// output bit; a clear direction bit samples the target and masks that output.
// The returned stream contains one sample per clock, correcting scan-v3 input
// alignment across calls for every product and firmware record. A complete
// response with
// a nonzero status returns *ScanError. Direct sessions require ConfigureSWD;
// probe-owned sessions reconfigure before the next valid request. An ambiguous
// transfer poisons the session and requires close and reopen. SWDIO never
// replays a scan.
func (s *Session) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if bits <= 0 || len(direction)*8 < bits || len(output)*8 < bits {
		return nil, fmt.Errorf("jlink: invalid %d-bit SWD stream", bits)
	}
	if err := s.transportReady(ctx); err != nil {
		return nil, err
	}
	if (!s.configured && !s.recoverableScan()) || s.info.SelectedInterface != interfaceSWD {
		return nil, errors.New("jlink: SWD is not configured")
	}
	if bits > s.transferBits {
		return nil, fmt.Errorf("jlink: %d-bit SWD stream exceeds %d-bit transfer limit", bits, s.transferBits)
	}
	if err := s.recoverScan(ctx); err != nil {
		return nil, err
	}
	control, drive := scanStreams(direction, output, bits)
	samples, err := s.scan(ctx, control, drive, bits, "SWD")
	if err != nil {
		return nil, err
	}
	if s.delayInput {
		samples, s.inputCarry = delayInputSamples(direction, samples, bits, s.inputCarry)
	}
	maskUnusedBits(samples, bits)
	return samples, nil
}

func (s *Session) scan(ctx context.Context, control, drive []byte, bits int, name string) ([]byte, error) {
	request := make([]byte, 4+len(control)+len(drive))
	request[0] = commandScanV3
	binary.LittleEndian.PutUint16(request[2:4], uint16(bits))
	copy(request[4:], control)
	copy(request[4+len(control):], drive)
	if err := s.writeExact(ctx, request); err != nil {
		return nil, fmt.Errorf("jlink: write %s scan command: %w", name, err)
	}
	samples, err := s.readResponsePart(ctx, len(control))
	if err != nil {
		return nil, fmt.Errorf("jlink: read %s scan samples: %w", name, err)
	}
	status, err := s.readResponse(ctx, 1)
	if err != nil {
		return nil, fmt.Errorf("jlink: read %s scan status: %w", name, err)
	}
	if status[0] != 0 {
		s.configured = false
		s.scanFailed = true
		s.inputCarry = false
		return nil, &ScanError{Status: status[0]}
	}
	maskUnusedBits(samples, bits)
	return samples, nil
}

func scanStreams(direction, output []byte, bits int) ([]byte, []byte) {
	size := (bits + 7) / 8
	control := append([]byte(nil), direction[:size]...)
	drive := make([]byte, size)
	for bit := range bits {
		if streamBit(direction, bit) && streamBit(output, bit) {
			drive[bit/8] |= 1 << uint(bit%8)
		}
	}
	maskUnusedBits(control, bits)
	return control, drive
}

func delayInputSamples(direction, samples []byte, bits int, carry bool) ([]byte, bool) {
	result := append([]byte(nil), samples...)
	for bit := range bits {
		if streamBit(direction, bit) {
			continue
		}
		setStreamBit(result, bit, carry)
		carry = streamBit(samples, bit)
	}
	return result, carry
}

func maskUnusedBits(stream []byte, bits int) {
	if remainder := bits % 8; remainder != 0 {
		stream[len(stream)-1] &= 1<<uint(remainder) - 1
	}
}

func streamBit(stream []byte, bit int) bool {
	return stream[bit/8]&(1<<uint(bit%8)) != 0
}

func setStreamBit(stream []byte, bit int, value bool) {
	mask := byte(1 << uint(bit%8))
	if value {
		stream[bit/8] |= mask
	} else {
		stream[bit/8] &^= mask
	}
}

func (s *Session) recoverableScan() bool {
	return s.probeOwned && s.scanFailed && s.RestorationPossible()
}

func (s *Session) recoverScan(ctx context.Context) error {
	if !s.recoverableScan() {
		return nil
	}
	var err error
	if s.info.SelectedInterface == interfaceSWD {
		err = s.ConfigureSWD(ctx, s.clockHz)
	} else {
		err = s.ConfigureJTAG(ctx, s.clockHz)
	}
	if err != nil && s.RestorationPossible() {
		s.scanFailed = true
	}
	return err
}
