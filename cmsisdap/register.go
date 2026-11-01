package cmsisdap

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/swd"
)

// RegisterSWD owns SWD framing and ORUNDETECT restoration over a borrowed
// configured session. Calls must be serialized. Give it exclusive use of the
// session until Release succeeds, then close the session separately.
type RegisterSWD struct {
	session         *Session
	active, known   bool
	originalOverrun bool
	identity        uint32
}

// NewRegisterSWD constructs an idle register transport without traffic. The
// session must be configured for SWD before Connect. Do not reconfigure it or
// send direct sequences while this transport owns protocol state.
func NewRegisterSWD(session *Session) *RegisterSWD { return &RegisterSWD{session: session} }

// Connect enters SWD, establishes bank zero, clears supported sticky state,
// and disables ORUNDETECT while preserving its first acquired setting.
// Failed entry retains any acquired restoration for Release with a fresh context.
func (r *RegisterSWD) Connect(ctx context.Context) (uint32, error) {
	state, err := r.prepare(ctx)
	if err != nil {
		return 0, err
	}
	if !r.known {
		r.originalOverrun, r.known = state&1 != 0, true
	}
	if state&1 != 0 {
		if err := r.setGrammar(ctx, true); err != nil {
			return 0, err
		}
		if _, _, err := r.session.transferRegister(ctx, 4, state&^1); err != nil {
			return 0, err
		}
		if err := r.setGrammar(ctx, false); err != nil {
			return 0, err
		}
	}
	if _, err := r.observeOverrun(ctx, false); err != nil {
		return 0, err
	}
	r.active = true
	return r.identity, nil
}

func (r *RegisterSWD) prepare(ctx context.Context) (uint32, error) {
	if err := r.ready(ctx); err != nil {
		return 0, err
	}
	r.active = false
	if err := r.statusCommand(ctx, []byte{4, 8, 0, 0, 0, 0}); err != nil {
		return 0, err
	}
	if err := r.setGrammar(ctx, false); err != nil {
		return 0, err
	}
	if err := swd.New(swjOutput{session: r.session}).JTAGToSWD(ctx); err != nil {
		return 0, err
	}
	id, _, err := r.session.transferRegister(ctx, 2, 0)
	if err != nil {
		return 0, err
	}
	if id&1 == 0 || r.known && id != r.identity {
		return 0, fmt.Errorf("cmsisdap: invalid or changed DPIDR %#x: %w", id, dap.ErrProtocol)
	}
	r.identity = id
	clear := uint32(0x1d)
	if id>>16&1 == 0 {
		clear |= 2
	}
	for _, write := range []struct {
		addr  byte
		value uint32
	}{{0, clear}, {8, 0}} {
		if _, _, err := r.session.transferRegister(ctx, write.addr, write.value); err != nil {
			return 0, err
		}
	}
	state, _, err := r.session.transferRegister(ctx, 6, 0)
	return state, err
}

func (r *RegisterSWD) ready(ctx context.Context) error {
	if r == nil {
		return errors.New("cmsisdap: nil register transport")
	}
	if err := r.session.transportReady(ctx); err != nil {
		return err
	}
	if !r.session.configured || r.session.packetSize < 19 {
		return errors.New("cmsisdap: register transport requires configured SWD and a 19-byte packet")
	}
	return nil
}

func (r *RegisterSWD) statusCommand(ctx context.Context, command []byte) error {
	response, err := r.session.exchange(ctx, command)
	if err != nil {
		return err
	}
	return errors.Join(r.session.validateStatusResponse(response, command[0]), ctx.Err())
}

func (r *RegisterSWD) setGrammar(ctx context.Context, overrun bool) error {
	var config byte
	if overrun {
		config = 4
	}
	return r.statusCommand(ctx, []byte{0x13, config})
}

// Release restores acquired ORUNDETECT state without disconnecting or closing
// the session. Live failures retain restoration for another attempt.
func (r *RegisterSWD) Release(ctx context.Context) error {
	if r == nil || !r.known {
		return nil
	}
	state, err := r.prepare(ctx)
	if err != nil {
		return err
	}
	if err := r.setGrammar(ctx, state&1 != 0); err != nil {
		return err
	}
	want := state &^ 1
	if r.originalOverrun {
		want |= 1
	}
	if _, _, err := r.session.transferRegister(ctx, 4, want); err != nil {
		return err
	}
	if err := r.setGrammar(ctx, r.originalOverrun); err != nil {
		return err
	}
	restored, err := r.observeOverrun(ctx, r.originalOverrun)
	if restored {
		r.known, r.active = false, false
	}
	return err
}

// CleanupPending reports only protocol restoration, independently of the
// session's port and host cleanup. It performs no I/O.
func (r *RegisterSWD) CleanupPending() bool { return r != nil && r.known }

func (r *RegisterSWD) transfer(ctx context.Context, addr, flags byte, value uint32) (uint32, dap.RegisterStatus, error) {
	if r == nil || !r.active || addr&^byte(12) != 0 {
		return 0, dap.RegisterUnsent, errors.New("cmsisdap: inactive register transport or invalid address")
	}
	result, status, err := r.session.transferRegister(ctx, addr|flags, value)
	if status == dap.RegisterIndeterminate && !errors.Is(err, dap.ErrFault) {
		r.active = false
	}
	return result, status, err
}

// ReadDP completes one physical DP read at offset 0, 4, 8 or 12.
func (r *RegisterSWD) ReadDP(ctx context.Context, addr uint8) (uint32, dap.RegisterStatus, error) {
	return r.transfer(ctx, addr, 2, 0)
}

// WriteDP completes one physical DP write, including its completion check.
func (r *RegisterSWD) WriteDP(ctx context.Context, addr uint8, value uint32) (dap.RegisterStatus, error) {
	_, status, err := r.transfer(ctx, addr, 0, value)
	return status, err
}

// ReadAP returns a completed AP value without exposing the posted response.
func (r *RegisterSWD) ReadAP(ctx context.Context, addr uint8) (uint32, dap.RegisterStatus, error) {
	return r.transfer(ctx, addr, 3, 0)
}

// WriteAP completes one physical AP write, including its completion check.
func (r *RegisterSWD) WriteAP(ctx context.Context, addr uint8, value uint32) (dap.RegisterStatus, error) {
	_, status, err := r.transfer(ctx, addr, 1, value)
	return status, err
}

func (r *RegisterSWD) observeOverrun(ctx context.Context, want bool) (bool, error) {
	state, status, err := r.session.transferRegister(ctx, 6, 0)
	if status != dap.RegisterCompleted {
		return false, err
	}
	if (state&1 != 0) != want {
		return false, errors.Join(err, errors.New("cmsisdap: ORUNDETECT did not change"))
	}
	return true, err
}
