package cmsisdap

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jon/ostiole/dap"
)

const commandTransfer = 0x05

func (s *Session) transferRegister(ctx context.Context, request byte, data uint32) (value uint32, status dap.RegisterStatus, err error) {
	if err := s.transportReady(ctx); err != nil {
		return 0, dap.RegisterUnsent, err
	}
	if request&0xf0 != 0 || !s.configured {
		return 0, dap.RegisterUnsent, errors.New("cmsisdap: invalid or unconfigured register transfer")
	}
	defer func() { err = errors.Join(err, ctx.Err()) }()
	command := []byte{commandTransfer, 0, 1, request}
	read := request&2 != 0
	if !read {
		command = binary.LittleEndian.AppendUint32(command, data)
	}
	if s.packetSize < len(command) || read && s.packetSize < 7 {
		return 0, dap.RegisterUnsent, errors.New("cmsisdap: packet cannot hold one register transfer")
	}
	response, err := s.exchange(ctx, command)
	if err != nil {
		if s.poisoned {
			return 0, dap.RegisterIndeterminate, errors.Join(err, dap.ErrIndeterminate)
		}
		return 0, dap.RegisterUnsent, err
	}
	return s.decodeRegisterResponse(response, request)
}

func (s *Session) decodeRegisterResponse(response []byte, request byte) (uint32, dap.RegisterStatus, error) {
	if len(response) == 1 && response[0] == statusError {
		return 0, dap.RegisterUnsent, errors.New("cmsisdap: DAP_Transfer is not implemented")
	}
	if len(response) < 3 || response[0] != commandTransfer || response[1] > 1 {
		return s.invalidRegisterResponse(response)
	}
	switch response[2] {
	case 1:
		return s.completedRegisterResponse(response, request&2 != 0)
	case 2:
		return s.failedRegisterResponse(response, request, dap.ErrWait)
	case 4:
		return s.failedRegisterResponse(response, request, dap.ErrFault)
	case 0, 3, 5, 6, 7, 8:
		err := fmt.Errorf("cmsisdap: target transfer status %#x: %w", response[2], dap.ErrProtocol)
		return 0, dap.RegisterIndeterminate, errors.Join(err, dap.ErrIndeterminate)
	default:
		return s.invalidRegisterResponse(response)
	}
}

func (s *Session) failedRegisterResponse(response []byte, request byte, cause error) (uint32, dap.RegisterStatus, error) {
	if response[1] == 0 {
		return 0, dap.RegisterRejected, cause
	}
	if request&2 != 0 {
		if request&1 == 0 {
			return s.invalidRegisterResponse(response)
		}
		if cause == dap.ErrFault {
			return 0, dap.RegisterCompleted, cause
		}
	}
	return 0, dap.RegisterIndeterminate, errors.Join(cause, dap.ErrIndeterminate)
}

func (s *Session) invalidRegisterResponse(response []byte) (uint32, dap.RegisterStatus, error) {
	err := fmt.Errorf("cmsisdap: invalid DAP_Transfer response %x: %w", response, dap.ErrProtocol)
	return 0, dap.RegisterIndeterminate, s.poison(errors.Join(err, dap.ErrIndeterminate))
}

func (s *Session) completedRegisterResponse(response []byte, read bool) (uint32, dap.RegisterStatus, error) {
	if response[1] != 1 || read && len(response) < 7 {
		return s.invalidRegisterResponse(response)
	}
	var value uint32
	if read {
		value = binary.LittleEndian.Uint32(response[3:7])
	}
	return value, dap.RegisterCompleted, nil
}
