// Package ftdi drives explicitly selected FTDI MPSSE ports.
package ftdi

import (
	"context"
	"errors"
	"fmt"
	"sync"

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

// ErrChannelPoisoned reports that an earlier ambiguous USB transfer left the
// MPSSE command stream unsafe to reuse. Close the channel and open a new one.
var ErrChannelPoisoned = errors.New("ftdi: channel is poisoned")

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

// Config selects one MPSSE port and the maximum requested SWD clock.
type Config struct {
	Port       Port
	MaxClockHz uint32
}

type usbDevice interface {
	Identity() usb.DeviceInfo
	claimInterface(iface uint8) (usbClaim, error)
	ControlTransfer(ctx context.Context, requestType, request uint8, value, index uint16, data []byte) (int, error)
	Close() error
}

type usbClaim interface {
	Endpoint(context.Context, uint8) (usb.Endpoint, error)
	SubmitBulk(context.Context, uint8, []byte) (usbBulkTransfer, error)
	AbortBulk(uint8) error
	Close() error
}

type usbBulkTransfer interface {
	Wait(context.Context) (int, error)
}

type ownedUSBDevice struct {
	*usb.Device
}

func (d ownedUSBDevice) claimInterface(iface uint8) (usbClaim, error) {
	claim, err := d.ClaimInterface(iface)
	if err != nil {
		return nil, err
	}
	return ownedUSBClaim{ClaimedInterface: claim}, nil
}

type ownedUSBClaim struct {
	*usb.ClaimedInterface
}

func (c ownedUSBClaim) SubmitBulk(ctx context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	return c.ClaimedInterface.SubmitBulk(ctx, endpoint, buffer)
}

// Channel addresses one explicit MPSSE-capable USB function.
type Channel struct {
	device      usbDevice
	iface       uint8
	index       uint16
	bulkIn      uint8
	bulkOut     uint8
	divisor     uint16
	clockHz     uint32
	packetSize  int
	claim       usbClaim
	receive     chan []byte
	receiveErr  chan error
	receiveStop chan struct{}
	receiveDone chan struct{}
	receiveMu   sync.Mutex
	receiving   bool
	settle      func(context.Context) error
	poisonErr   error
}

func newChannel(device usbDevice, config Config) (*Channel, error) {
	if device == nil {
		return nil, errors.New("ftdi: nil USB device")
	}
	if config.Port != PortA && config.Port != PortB {
		return nil, errors.New("ftdi: port A or B is required")
	}
	if err := validateSelection(device.Identity(), config.Port); err != nil {
		return nil, err
	}
	divisor, err := clockDivisor(config.MaxClockHz)
	if err != nil {
		return nil, err
	}
	iface := uint8(config.Port - PortA)
	return &Channel{
		device:  device,
		iface:   iface,
		index:   uint16(iface) + 1,
		bulkIn:  0x81 + 2*iface,
		bulkOut: 0x02 + 2*iface,
		divisor: divisor,
		clockHz: baseClockHz / (2 * (uint32(divisor) + 1)),
		settle:  settleMPSSE,
	}, nil
}

func validateSelection(identity usb.DeviceInfo, port Port) error {
	if identity.VID != VID {
		return fmt.Errorf("ftdi: unsupported USB vendor %#04x", identity.VID)
	}
	switch identity.PID {
	case PIDFT232H:
		if port != PortA {
			return errors.New("ftdi: FT232H supports only port A")
		}
	case PIDFT2232H, PIDFT4232H:
	default:
		return fmt.Errorf("ftdi: unsupported USB product %#04x", identity.PID)
	}
	return nil
}

// ClockHz reports the SWD clock selected during Open.
func (c *Channel) ClockHz() uint32 {
	if c == nil {
		return 0
	}
	return c.clockHz
}

func (c *Channel) claimUSB() error {
	claim, err := c.device.claimInterface(c.iface)
	if err != nil {
		return err
	}
	c.claim = claim
	return nil
}

type usbRead struct {
	buffer   []byte
	transfer usbBulkTransfer
}

func (c *Channel) openUSBTransfers(ctx context.Context) error {
	endpoint, err := c.claim.Endpoint(ctx, c.bulkIn)
	if err != nil {
		return err
	}
	if endpoint.TransferType != usb.TransferBulk || endpoint.MaxPacketSize <= packetStatusSize {
		return fmt.Errorf("ftdi: invalid bulk IN endpoint %#02x", c.bulkIn)
	}
	c.packetSize = int(endpoint.MaxPacketSize)
	depth := receiveDepth((maxSWDTransferBits+1)/2, c.packetSize)
	c.receive = make(chan []byte, 1)
	c.receiveErr = make(chan error, 1)
	c.receiveStop = make(chan struct{})
	c.receiveDone = make(chan struct{})
	reads := make([]usbRead, 0, depth)
	for range depth {
		read, err := c.submitUSBRead(ctx, make([]byte, c.packetSize))
		if err != nil {
			abortErr := c.claim.AbortBulk(c.bulkIn)
			c.receive, c.receiveErr = nil, nil
			c.receiveStop, c.receiveDone = nil, nil
			return errors.Join(err, abortErr)
		}
		reads = append(reads, read)
	}
	go c.receiveUSB(reads)
	return nil
}

func (c *Channel) releaseUSB() error {
	if c.claim == nil {
		return nil
	}
	if c.receiveStop != nil {
		select {
		case <-c.receiveStop:
		default:
			close(c.receiveStop)
		}
		if err := c.claim.AbortBulk(c.bulkIn); err != nil {
			return err
		}
		<-c.receiveDone
		c.receive, c.receiveErr = nil, nil
		c.receiveStop, c.receiveDone = nil, nil
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
	if c.claim == nil || c.receiveStop == nil {
		return 0, errors.New("ftdi: USB transfers are not open")
	}
	transfer, err := c.claim.SubmitBulk(ctx, c.bulkOut, data)
	if err != nil {
		return 0, err
	}
	count, err := transfer.Wait(ctx)
	if err == nil || ctx.Err() == nil {
		return count, err
	}
	abortErr := c.claim.AbortBulk(c.bulkOut)
	if abortErr != nil {
		return 0, errors.Join(ctx.Err(), abortErr)
	}
	_, completionErr := transfer.Wait(context.Background())
	return 0, errors.Join(ctx.Err(), completionErr)
}

func (c *Channel) receiveUSB(reads []usbRead) {
	defer close(c.receiveDone)
	for {
		raw, replacement, stopped, err := c.rearmUSBRead(reads[0])
		if stopped {
			return
		}
		if err != nil {
			c.reportReceiveError(err)
			return
		}
		reads = append(reads[1:], replacement)
		stopped, err = c.deliverUSBRead(raw)
		if stopped {
			return
		}
		if err != nil {
			c.reportReceiveError(err)
			return
		}
	}
}

func (c *Channel) rearmUSBRead(read usbRead) ([]byte, usbRead, bool, error) {
	count, err := read.transfer.Wait(context.Background())
	if err != nil {
		return nil, usbRead{}, c.receiveStopped(), err
	}
	if count < 0 || count > len(read.buffer) {
		return nil, usbRead{}, false, fmt.Errorf("ftdi: invalid bulk-read count %d for %d-byte buffer", count, len(read.buffer))
	}
	raw := append([]byte(nil), read.buffer[:count]...)
	if c.receiveStopped() {
		return nil, usbRead{}, true, nil
	}
	replacement, err := c.submitUSBRead(context.Background(), read.buffer)
	return raw, replacement, false, err
}

func (c *Channel) deliverUSBRead(raw []byte) (bool, error) {
	payload, err := decodeCompletion(raw, c.packetSize)
	if err != nil || len(payload) == 0 {
		return false, err
	}
	if !c.responsePending() {
		return false, errors.New("ftdi: received payload without a pending response")
	}
	select {
	case c.receive <- payload:
		return false, nil
	case <-c.receiveStop:
		return true, nil
	}
}

func (c *Channel) receiveStopped() bool {
	select {
	case <-c.receiveStop:
		return true
	default:
		return false
	}
}

func (c *Channel) submitUSBRead(ctx context.Context, buffer []byte) (usbRead, error) {
	transfer, err := c.claim.SubmitBulk(ctx, c.bulkIn, buffer)
	if err != nil {
		return usbRead{}, err
	}
	return usbRead{buffer: buffer, transfer: transfer}, nil
}

func (c *Channel) reportReceiveError(err error) {
	c.recordPoison(err)
	select {
	case c.receiveErr <- err:
	case <-c.receiveStop:
	}
}

func (c *Channel) responsePending() bool {
	c.receiveMu.Lock()
	defer c.receiveMu.Unlock()
	return c.receiving
}

func (c *Channel) beginResponse() error {
	c.receiveMu.Lock()
	defer c.receiveMu.Unlock()
	select {
	case err := <-c.receiveErr:
		return err
	case payload := <-c.receive:
		return fmt.Errorf("ftdi: received %d stale payload bytes", len(payload))
	default:
	}
	c.receiving = true
	return nil
}

func (c *Channel) endResponse() {
	c.receiveMu.Lock()
	c.receiving = false
	c.receiveMu.Unlock()
}

func (c *Channel) transportReady() error {
	if c == nil || c.device == nil {
		return errors.New("ftdi: nil channel")
	}
	c.receiveMu.Lock()
	err := c.poisonErr
	c.receiveMu.Unlock()
	if err != nil {
		return errors.Join(err, ErrChannelPoisoned)
	}
	return nil
}

func (c *Channel) poison(err error) error {
	if err == nil {
		return nil
	}
	c.recordPoison(err)
	c.receiveMu.Lock()
	first := c.poisonErr
	c.receiveMu.Unlock()
	return errors.Join(first, ErrChannelPoisoned)
}

func (c *Channel) recordPoison(err error) {
	if err == nil {
		return
	}
	c.receiveMu.Lock()
	if c.poisonErr == nil {
		c.poisonErr = err
	}
	c.receiveMu.Unlock()
}
