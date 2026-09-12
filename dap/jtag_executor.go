package dap

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jon/ostiole/jtag"
)

type jtagExecutor struct {
	dp            *DebugPort
	chain         *jtag.Chain
	index, ir     int
	tap           jtag.TAP
	ownedOverrun  bool
	abortRequired bool
	faultPending  bool
}

func (e *jtagExecutor) validate() error {
	layout := e.chain.Layout()
	if e.index < 0 || e.index >= len(layout) {
		return errors.New("dap: JTAG TAP index outside the expected chain")
	}
	e.ir = layout[e.index].IRBits()
	if e.ir != 4 && e.ir != 8 {
		return errors.New("dap: JTAG-DP requires a four- or eight-bit instruction register")
	}
	if layout[e.index].ResetRegister().Bypass {
		return errors.New("dap: JTAG-DP requires an explicit IDCODE")
	}
	return nil
}

func (e *jtagExecutor) scan(ctx context.Context, instruction byte, data []byte, bits int) ([]byte, error) {
	if e.ir == 8 {
		instruction |= 0xf0
	}
	if _, err := e.tap.ScanIR(ctx, []byte{instruction}); err != nil {
		e.dp.state.loseFraming()
		return nil, err
	}
	capture, err := e.tap.ScanDR(ctx, data, bits)
	if err != nil {
		e.dp.state.loseFraming()
	}
	return capture, err
}

func (e *jtagExecutor) exchange(ctx context.Context, req transferRequest, data uint32) (uint32, byte, error) {
	instruction := byte(0xa)
	if req.AP {
		instruction = 0xb
	}
	value := uint64(data)<<3 | uint64(req.Addr>>2)<<1
	if req.Read {
		value |= 1
	}
	var frame [8]byte
	binary.LittleEndian.PutUint64(frame[:], value)
	capture, err := e.scan(ctx, instruction, frame[:5], 35)
	if err != nil {
		return 0, 0, err
	}
	copy(frame[:], capture)
	value = binary.LittleEndian.Uint64(frame[:])
	return uint32(value >> 3), byte(value & 7), nil
}

// poll shifts next only until it is accepted. A WAIT discards that scan's
// request and leaves the preceding operation in flight.
func (e *jtagExecutor) poll(ctx context.Context, next transferRequest, data uint32) (uint32, error) {
	for waits := uint(0); ; {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		value, ack, err := e.exchange(ctx, next, data)
		if err != nil {
			return 0, err
		}
		switch ack {
		case 2:
			return value, nil
		case 1:
			waits++
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if e.dp.maxWaits != 0 && waits >= e.dp.maxWaits {
				return 0, ErrWait
			}
		default:
			e.dp.state.loseFraming()
			return 0, fmt.Errorf("dap: JTAG acknowledgement %#x: %w", ack, ErrProtocol)
		}
	}
}

func (e *jtagExecutor) transfer(ctx context.Context, req transferRequest, data uint32) transferResult {
	if err := ctx.Err(); err != nil {
		return transferResult{cause: err, outcome: transferUnsent}
	}
	if _, err := e.poll(ctx, req, data); err != nil {
		outcome := transferUnsent
		if e.dp.state.response == responseLost {
			outcome = transferIndeterminate
		}
		return transferResult{cause: err, outcome: outcome}
	}
	value, err := e.poll(ctx, dpTransferRequest(RDBUFF, true), 0)
	if err != nil {
		outcome := transferInFlight
		if e.dp.state.response == responseLost {
			outcome = transferIndeterminate
		}
		return transferResult{cause: err, outcome: outcome}
	}
	return transferResult{data: value, outcome: transferConfirmed}
}

func jtagResultError(result transferResult) error {
	if result.cause != nil && (result.outcome == transferInFlight || result.outcome == transferIndeterminate) {
		return errors.Join(result.cause, ErrIndeterminate)
	}
	return result.cause
}

func (e *jtagExecutor) abort(ctx context.Context) error {
	_, err := e.scan(ctx, 8, []byte{8, 0, 0, 0, 0}, 35)
	return err
}

func (e *jtagExecutor) prime(ctx context.Context) error {
	if _, err := e.poll(ctx, dpTransferRequest(RDBUFF, true), 0); err != nil {
		return err
	}
	_, err := e.poll(ctx, dpTransferRequest(RDBUFF, true), 0)
	return err
}

func (e *jtagExecutor) abortPending(ctx context.Context) error {
	if !e.abortRequired {
		return nil
	}
	if err := e.abort(ctx); err != nil {
		return err
	}
	e.abortRequired = false
	return nil
}

func (e *jtagExecutor) idcode(ctx context.Context) (uint32, error) {
	capture, err := e.scan(ctx, 0xe, make([]byte, 4), 32)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(capture), nil
}
