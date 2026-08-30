package cmsisdap

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// ConfigureSWD connects the probe's advertised SWD port and requests a
// maximum target clock. Reconfiguration disconnects an active port first. An
// error can leave cleanup pending; Close retries a complete disconnect failure.
func (s *Session) ConfigureSWD(ctx context.Context, maxClockHz uint32) error {
	if err := s.validateSWDConfiguration(ctx, maxClockHz); err != nil {
		return err
	}
	if s.connected {
		if err := s.disconnect(ctx); err != nil {
			return fmt.Errorf("cmsisdap: disconnect before SWD reconfiguration: %w", err)
		}
	}
	if err := s.connectSWD(ctx); err != nil {
		return err
	}
	if err := s.requestSWDClock(ctx, maxClockHz); err != nil {
		return s.configureFailure(ctx, err)
	}
	s.configured = true
	s.maxClockHz = maxClockHz
	return nil
}

func (s *Session) validateSWDConfiguration(ctx context.Context, maxClockHz uint32) error {
	if maxClockHz == 0 {
		return errors.New("cmsisdap: SWD clock ceiling must be greater than zero")
	}
	if err := s.transportReady(ctx); err != nil {
		return err
	}
	if !s.info.Capabilities.Has(CapabilitySWD) {
		return errors.New("cmsisdap: probe does not advertise SWD")
	}
	if s.packetSize < 5 {
		return fmt.Errorf("cmsisdap: packet size %d cannot hold DAP_SWJ_Clock", s.packetSize)
	}
	return nil
}

func (s *Session) connectSWD(ctx context.Context) error {
	s.connected = true
	response, err := s.exchange(ctx, []byte{commandConnect, portSWD})
	if err != nil {
		if !s.poisoned {
			s.connected = false
		}
		return fmt.Errorf("cmsisdap: DAP_Connect(SWD): %w", err)
	}
	if err := s.validateCommandResponse(response, commandConnect, 2); err != nil {
		if len(response) == 1 && response[0] == statusError {
			s.connected = false
			return fmt.Errorf("cmsisdap: DAP_Connect(SWD): %w", err)
		}
		return s.configureFailure(ctx, fmt.Errorf("cmsisdap: DAP_Connect(SWD): %w", err))
	}
	if response[1] == 0 {
		s.connected = false
		return errors.New("cmsisdap: DAP_Connect(SWD) did not initialize a port")
	}
	if response[1] != portSWD {
		return s.configureFailure(ctx, fmt.Errorf("cmsisdap: DAP_Connect selected port %d, want SWD", response[1]))
	}
	return nil
}

func (s *Session) requestSWDClock(ctx context.Context, maxClockHz uint32) error {
	request := []byte{commandSWJClock, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(request[1:], maxClockHz)
	response, err := s.exchange(ctx, request)
	if err != nil {
		return fmt.Errorf("cmsisdap: DAP_SWJ_Clock: %w", err)
	}
	if err := s.validateStatusResponse(response, commandSWJClock); err != nil {
		return fmt.Errorf("cmsisdap: DAP_SWJ_Clock: %w", err)
	}
	return nil
}

func (s *Session) configureFailure(ctx context.Context, primary error) error {
	if !s.connected || s.poisoned {
		return primary
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	err := s.disconnect(cleanupCtx)
	cancel()
	if err != nil {
		err = fmt.Errorf("cmsisdap: DAP_Disconnect after failed SWD configuration: %w", err)
	}
	return errors.Join(primary, err)
}

func (s *Session) disconnect(ctx context.Context) error {
	response, err := s.exchange(ctx, []byte{commandDisconnect})
	if err != nil {
		return fmt.Errorf("cmsisdap: DAP_Disconnect: %w", err)
	}
	if err := s.validateStatusResponse(response, commandDisconnect); err != nil {
		return fmt.Errorf("cmsisdap: DAP_Disconnect: %w", err)
	}
	s.clearSWDConfiguration()
	return nil
}

func (s *Session) clearSWDConfiguration() {
	s.connected = false
	s.configured = false
	s.maxClockHz = 0
}

func (s *Session) validateCommandResponse(response []byte, command byte, minimum int) error {
	if len(response) != 0 && response[0] == statusError {
		return fmt.Errorf("command %#02x is not implemented", command)
	}
	if len(response) < minimum {
		return s.poison(fmt.Errorf("short response: got %d bytes, need %d", len(response), minimum))
	}
	if response[0] != command {
		return s.poison(fmt.Errorf("response command = %#02x, want %#02x", response[0], command))
	}
	return nil
}

func (s *Session) validateStatusResponse(response []byte, command byte) error {
	if err := s.validateCommandResponse(response, command, 2); err != nil {
		return err
	}
	if response[1] != statusOK {
		return fmt.Errorf("status = %#02x, want %#02x", response[1], statusOK)
	}
	return nil
}
