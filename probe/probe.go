// Package probe owns a specific debug probe and lends its protocol surfaces.
package probe

import (
	"context"
	"errors"

	"github.com/jon/ostiole/swd"
)

// Info is detached display metadata for an owned probe.
type Info struct {
	Product, Serial, Function, Location string
}

// Backend owns the implementation and all of its resources. A failed Close
// must retain outstanding cleanup so another Close can retry it, or implement
// CleanupState to distinguish a final error after releasing its resources.
type Backend interface {
	Close() error
}

// RestorationState optionally reports whether communication needed to restore
// probe or target state remains possible. While restoration is outstanding,
// false requires terminal loss, such as device disappearance or a poisoned
// command stream. A timeout, target FAULT,
// authentication failure, or recoverable scan-status error alone is insufficient.
// The query performs no I/O and does not report whether restoration has occurred.
type RestorationState interface {
	RestorationPossible() bool
}

// CleanupState optionally reports whether an owner still has resources or
// restoration work requiring Close. False after a failed Close means that its
// error is final and another Close is unnecessary. The query performs no I/O.
type CleanupState interface {
	CleanupPending() bool
}

// Wire supplies SWD bits and the implementation's transfer limit.
type Wire interface {
	swd.Wire
	swd.TransferLimits
}

// SWDBackend is implemented by owners which can configure SWD. Even after an
// error, the backend retains every resource for Close. A successful call must
// return a non-nil wire. Implementations must not return typed nil interfaces.
type SWDBackend interface {
	Backend
	SWD(context.Context, SWDConfig) (Wire, error)
}

// SWDConfig specifies the requested target-clock ceiling, at least 1 kHz.
type SWDConfig struct {
	MaxClockHz uint32
}

// ErrUnsupportedSWD reports that the owned implementation cannot supply SWD.
var ErrUnsupportedSWD = errors.New("probe: SWD is not supported")

// Probe owns one implementation. Calls on it and its borrowed surfaces must
// be serialized. Its zero value owns nothing and cannot activate a protocol.
type Probe struct {
	info    Info
	backend Backend
	wire    Wire
	jtag    JTAGWire
	closing bool
}

// New takes ownership of backend without I/O. The caller must stop using it
// directly. A nil backend creates an inactive owner; typed nils are invalid.
func New(info Info, backend Backend) *Probe {
	return &Probe{info: info, backend: backend}
}

// Info returns a copy of the owner's display metadata.
func (p *Probe) Info() Info {
	if p == nil {
		return Info{}
	}
	return p.info
}

// Close invalidates borrowed surfaces and closes the implementation. A failed
// close retains the implementation for retry unless CleanupState confirms that
// cleanup finished. The encountered error is still returned. Once released,
// further calls do nothing. Releasing host resources does not restore a halted
// target.
func (p *Probe) Close() error {
	if p == nil {
		return nil
	}
	p.closing = true
	p.wire = nil
	p.jtag = nil
	if p.backend == nil {
		return nil
	}
	err := p.backend.Close()
	if err != nil && p.CleanupPending() {
		return err
	}
	p.backend = nil
	return err
}

// RestorationPossible reports whether restoration communication remains
// possible, without I/O. Backends without RestorationState conservatively
// report true while owned. Empty and released owners report false.
func (p *Probe) RestorationPossible() bool {
	if p == nil || p.backend == nil {
		return false
	}
	if state, ok := p.backend.(RestorationState); ok {
		return state.RestorationPossible()
	}
	return true
}

// CleanupPending reports whether Close still has work to do, without I/O.
// Backends without CleanupState conservatively report true while owned.
// Empty and released owners report false.
func (p *Probe) CleanupPending() bool {
	if p == nil || p.backend == nil {
		return false
	}
	if state, ok := p.backend.(CleanupState); ok {
		return state.CleanupPending()
	}
	return true
}

// SWD configures and lends a wire. Failed activation attempts cleanup and
// leaves Close available for retry. Reconfiguration requires a fresh owner.
func (p *Probe) SWD(ctx context.Context, config SWDConfig) (SWD, error) {
	if err := p.canActivate(ctx, config.MaxClockHz); err != nil {
		return SWD{}, err
	}
	b, ok := p.backend.(SWDBackend)
	if !ok {
		return SWD{}, ErrUnsupportedSWD
	}
	wire, err := b.SWD(ctx, config)
	if err := p.activationError(wire != nil, err); err != nil {
		return SWD{}, err
	}
	p.wire = wire
	return SWD{probe: p}, nil
}

func (p *Probe) canActivate(ctx context.Context, hz uint32) error {
	if ctx == nil {
		return errors.New("probe: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if hz < 1000 {
		return errors.New("probe: clock ceiling must be at least 1 kHz")
	}
	if p == nil || p.backend == nil || p.closing || p.wire != nil || p.jtag != nil {
		return errors.New("probe: owner is not available for activation")
	}
	return nil
}

func (p *Probe) activationError(hasWire bool, err error) error {
	if err == nil && !hasWire {
		err = errors.New("probe: backend returned no wire")
	}
	if err != nil {
		return errors.Join(err, p.Close())
	}
	return nil
}

// SWD is a borrowed wire. Its Probe must outlive every call on it.
type SWD struct{ probe *Probe }

// SWDIO delegates a direction-explicit transfer to the owning implementation.
func (w SWD) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if w.probe == nil || w.probe.wire == nil {
		return nil, errors.New("probe: inactive SWD wire")
	}
	return w.probe.wire.SWDIO(ctx, direction, output, bits)
}

// MaxTransferBits returns the active wire's limit, or zero after close.
func (w SWD) MaxTransferBits() int {
	if w.probe == nil || w.probe.wire == nil {
		return 0
	}
	return w.probe.wire.MaxTransferBits()
}
