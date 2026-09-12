package probe

import (
	"context"
	"errors"

	"github.com/jon/ostiole/jtag"
)

// JTAGWire supplies packed JTAG clocks and the implementation's transfer limit.
type JTAGWire interface {
	jtag.Wire
	jtag.TransferLimits
}

// JTAGBackend configures JTAG while retaining all resources for Close, even
// after failure. Success must return a non-nil wire; typed nils are invalid.
type JTAGBackend interface {
	Backend
	JTAG(context.Context, JTAGConfig) (JTAGWire, error)
}

// JTAGConfig specifies the requested TCK ceiling, at least 1 kHz.
type JTAGConfig struct {
	MaxClockHz uint32
}

// ErrUnsupportedJTAG reports that the implementation cannot supply JTAG.
var ErrUnsupportedJTAG = errors.New("probe: JTAG is not supported")

// JTAG configures and lends a wire. The owner cannot activate another protocol.
// Failed activation attempts cleanup and leaves Close available for retry.
// Reconfiguration requires a fresh owner. The caller verifies target wiring.
func (p *Probe) JTAG(ctx context.Context, config JTAGConfig) (JTAG, error) {
	if err := p.canActivate(ctx, config.MaxClockHz); err != nil {
		return JTAG{}, err
	}
	b, ok := p.backend.(JTAGBackend)
	if !ok {
		return JTAG{}, ErrUnsupportedJTAG
	}
	wire, err := b.JTAG(ctx, config)
	if err := p.activationError(wire != nil, err); err != nil {
		return JTAG{}, err
	}
	p.jtag = wire
	return JTAG{probe: p}, nil
}

// JTAG is a borrowed wire. Its Probe must outlive all wire and chain calls.
type JTAG struct{ probe *Probe }

// JTAGIO delegates packed TMS/TDI clocks to the owning implementation.
func (w JTAG) JTAGIO(ctx context.Context, tms, tdi []byte, bits int) ([]byte, error) {
	if w.probe == nil || w.probe.jtag == nil {
		return nil, errors.New("probe: inactive JTAG wire")
	}
	return w.probe.jtag.JTAGIO(ctx, tms, tdi, bits)
}

// MaxTransferBits returns the active wire's limit, or zero after close.
func (w JTAG) MaxTransferBits() int {
	if w.probe == nil || w.probe.jtag == nil {
		return 0
	}
	return w.probe.jtag.MaxTransferBits()
}
