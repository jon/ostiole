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
// must retain outstanding cleanup so another Close can retry it.
type Backend interface {
	Close() error
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
// close retains the implementation for retry. After a successful close, further
// calls do nothing.
func (p *Probe) Close() error {
	if p == nil {
		return nil
	}
	p.closing = true
	p.wire = nil
	if p.backend == nil {
		return nil
	}
	if err := p.backend.Close(); err != nil {
		return err
	}
	p.backend = nil
	return nil
}

// SWD configures and lends a wire. Failed activation attempts cleanup and
// leaves Close available for retry. Reconfiguration requires a fresh owner.
func (p *Probe) SWD(ctx context.Context, config SWDConfig) (SWD, error) {
	if ctx == nil {
		return SWD{}, errors.New("probe: nil context")
	}
	if err := ctx.Err(); err != nil {
		return SWD{}, err
	}
	if config.MaxClockHz < 1000 {
		return SWD{}, errors.New("probe: clock ceiling must be at least 1 kHz")
	}
	if p == nil || p.backend == nil || p.closing || p.wire != nil {
		return SWD{}, errors.New("probe: owner is not available for activation")
	}
	b, ok := p.backend.(SWDBackend)
	if !ok {
		return SWD{}, ErrUnsupportedSWD
	}
	wire, err := b.SWD(ctx, config)
	if err == nil && wire == nil {
		err = errors.New("probe: backend returned no wire")
	}
	if err != nil {
		return SWD{}, errors.Join(err, p.Close())
	}
	p.wire = wire
	return SWD{probe: p}, nil
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
