// Package ftdi drives explicitly selected FTDI MPSSE ports.
package ftdi

import (
	"context"
	"errors"
	"fmt"

	"github.com/jon/ostiole/usb"
)

const (
	// VID is FTDI's USB vendor identifier.
	VID = 0x0403

	// PIDFT2232H identifies an FT2232H USB attachment.
	PIDFT2232H = 0x6010
	// PIDFT4232H identifies an FT4232H USB attachment.
	PIDFT4232H = 0x6011
	// PIDFT232H identifies an FT232H USB attachment.
	PIDFT232H = 0x6014

	requestTypeOut = 0x40
)

// Port identifies an MPSSE-capable function on an FTDI device.
type Port uint8

const (
	// PortUnspecified leaves the MPSSE port unselected.
	PortUnspecified Port = iota
	// PortA selects the first MPSSE-capable function.
	PortA
	// PortB selects the second MPSSE-capable function.
	PortB
)

// Interface identifies the wire interface assigned to a port.
type Interface uint8

const (
	// InterfaceUnspecified leaves the wire interface unselected.
	InterfaceUnspecified Interface = iota
	// SWD selects the Serial Wire Debug interface.
	SWD
)

// Config explicitly selects one FTDI product, port, interface, and clock.
type Config struct {
	ClockHz   uint32
	ProductID uint16
	Port      Port
	Interface Interface
}

type usbDevice interface {
	claimInterface(iface uint8) (usbClaim, error)
	ControlTransfer(ctx context.Context, requestType, request uint8, value, index uint16, data []byte) (int, error)
	BulkWrite(ctx context.Context, endpoint uint8, data []byte) (int, error)
	BulkRead(ctx context.Context, endpoint uint8, data []byte) (int, error)
	Close() error
}

type usbClaim interface {
	Close() error
}

type ownedUSBDevice struct {
	*usb.Device
}

func (d ownedUSBDevice) claimInterface(iface uint8) (usbClaim, error) {
	return d.ClaimInterface(iface)
}

// Channel addresses one explicit MPSSE-capable USB function.
type Channel struct {
	device     usbDevice
	iface      uint8
	index      uint16
	bulkIn     uint8
	bulkOut    uint8
	clockHz    uint32
	packetSize int
	claim      usbClaim
	settle     func(context.Context) error
}

func newChannel(device usbDevice, config Config) (*Channel, error) {
	if device == nil {
		return nil, errors.New("ftdi: nil USB device")
	}
	if config.Interface != SWD {
		return nil, errors.New("ftdi: SWD interface is required")
	}
	if config.Port != PortA && config.Port != PortB {
		return nil, errors.New("ftdi: port A or B is required")
	}
	if err := validateSelection(config.ProductID, config.Port); err != nil {
		return nil, err
	}
	iface := uint8(config.Port - PortA)
	return &Channel{
		device:     device,
		iface:      iface,
		index:      uint16(iface) + 1,
		bulkIn:     0x81 + 2*iface,
		bulkOut:    0x02 + 2*iface,
		clockHz:    config.ClockHz,
		packetSize: 512,
		settle:     settleMPSSE,
	}, nil
}

func validateSelection(product uint16, port Port) error {
	switch product {
	case PIDFT232H:
		if port != PortA {
			return errors.New("ftdi: FT232H supports only port A")
		}
	case PIDFT2232H, PIDFT4232H:
	default:
		return fmt.Errorf("ftdi: unsupported USB product %#04x", product)
	}
	return nil
}

func (c *Channel) claimUSB() error {
	claim, err := c.device.claimInterface(c.iface)
	if err != nil {
		return err
	}
	c.claim = claim
	return nil
}

func (c *Channel) releaseUSB() error {
	if c.claim == nil {
		return nil
	}
	if err := c.claim.Close(); err != nil {
		return err
	}
	c.claim = nil
	return nil
}

func (c *Channel) control(ctx context.Context, request uint8, value uint16) (int, error) {
	return c.device.ControlTransfer(ctx, requestTypeOut, request, value, c.index, nil)
}

func (c *Channel) bulkWrite(ctx context.Context, data []byte) (int, error) {
	return c.device.BulkWrite(ctx, c.bulkOut, data)
}

func (c *Channel) bulkRead(ctx context.Context, data []byte) (int, error) {
	return c.device.BulkRead(ctx, c.bulkIn, data)
}
