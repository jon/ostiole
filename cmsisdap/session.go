package cmsisdap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jon/ostiole/usb"
)

type usbDevice interface {
	Identity() usb.DeviceInfo
	ActiveConfiguration(context.Context) (usb.Configuration, error)
	claimInterface(uint8) (usbClaim, error)
	Close() error
}

type usbClaim interface {
	SetAltSetting(uint8) error
	Endpoint(context.Context, uint8) (usb.Endpoint, error)
	SubmitBulk(context.Context, uint8, []byte) (usbBulkTransfer, error)
	AbortBulk(uint8) error
	Close() error
}

type usbBulkTransfer interface {
	Wait(context.Context) (int, error)
}

type ownedUSBDevice struct{ *usb.Device }

func (d ownedUSBDevice) claimInterface(number uint8) (usbClaim, error) {
	claim, err := d.ClaimInterface(number)
	if err != nil {
		return nil, err
	}
	return ownedUSBClaim{ClaimedInterface: claim}, nil
}

type ownedUSBClaim struct{ *usb.ClaimedInterface }

func (c ownedUSBClaim) SubmitBulk(ctx context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	return c.ClaimedInterface.SubmitBulk(ctx, endpoint, buffer)
}

// Session owns one CMSIS-DAP v2 command interface and its USB device. Calls on
// a session must be serialized.
type Session struct {
	device      usbDevice
	claim       usbClaim
	command     commandInterface
	packetSize  int
	info        Info
	poisoned    bool
	connected   bool
	configured  bool
	maxClockHz  uint32
	closePrefix error
	closeDone   bool
	closeErr    error
}

// Open claims the CMSIS-DAP v2 command interface, reads probe metadata, applies
// its options, and takes ownership of the device on success. With no options it
// does not connect a target port. After an error, close a returned non-nil
// session; when Open returns no session, close device. Open has already
// attempted cleanup.
func Open(ctx context.Context, device *usb.Device, options ...Option) (*Session, error) {
	if device == nil {
		return nil, errors.New("cmsisdap: nil USB device")
	}
	return openSession(ctx, ownedUSBDevice{Device: device}, options...)
}

func openSession(ctx context.Context, device usbDevice, options ...Option) (result *Session, err error) {
	if device == nil {
		return nil, errors.New("cmsisdap: nil USB device")
	}
	config, err := prepareOpen(ctx, options)
	if err != nil {
		return nil, errors.Join(err, device.Close())
	}
	configuration, err := device.ActiveConfiguration(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("cmsisdap: inspect active USB configuration: %w", err), device.Close())
	}
	command, err := findCommandInterface(configuration)
	if err != nil {
		return nil, errors.Join(err, device.Close())
	}
	session := &Session{device: device, command: command, info: Info{USB: device.Identity()}}
	defer func() {
		result, err = settleOpenFailure(session, result, err)
	}()
	claim, err := device.claimInterface(command.number)
	if err != nil {
		return nil, fmt.Errorf("cmsisdap: claim USB interface %d: %w", command.number, err)
	}
	session.claim = claim
	if err := claim.SetAltSetting(command.alternate); err != nil {
		return nil, fmt.Errorf("cmsisdap: select USB interface %d alternate %d: %w", command.number, command.alternate, err)
	}
	bulkOut, err := selectedEndpoint(ctx, claim, command.bulkOut.Address, false)
	if err != nil {
		return nil, err
	}
	bulkIn, err := selectedEndpoint(ctx, claim, command.bulkIn.Address, true)
	if err != nil {
		return nil, err
	}
	session.command.bulkOut, session.command.bulkIn = bulkOut, bulkIn
	session.packetSize = int(bulkIn.MaxPacketSize)
	if session.packetSize < minimumPacketSize {
		return nil, fmt.Errorf("cmsisdap: active bulk IN packet size = %d, want at least %d", session.packetSize, minimumPacketSize)
	}
	if err := session.readInfo(ctx); err != nil {
		return nil, err
	}
	return finishOpenSession(ctx, session, config)
}

func settleOpenFailure(session, result *Session, err error) (*Session, error) {
	if err == nil {
		return result, nil
	}
	retain, cleanupErr := session.closeAfterOpenFailure()
	if retain {
		result = session
	}
	return result, errors.Join(err, cleanupErr)
}

func finishOpenSession(ctx context.Context, session *Session, config openConfig) (*Session, error) {
	if !config.configureSWD {
		return session, nil
	}
	if err := session.ConfigureSWD(ctx, config.maxClockHz); err != nil {
		return nil, err
	}
	return session, nil
}

func prepareOpen(ctx context.Context, options []Option) (openConfig, error) {
	if ctx == nil {
		return openConfig{}, errors.New("cmsisdap: nil open context")
	}
	if err := ctx.Err(); err != nil {
		return openConfig{}, err
	}
	var config openConfig
	for index, option := range options {
		if option.apply == nil {
			continue
		}
		if err := option.apply(&config); err != nil {
			return openConfig{}, fmt.Errorf("cmsisdap: option %d: %w", index, err)
		}
	}
	return config, nil
}

func selectedEndpoint(ctx context.Context, claim usbClaim, address uint8, input bool) (usb.Endpoint, error) {
	endpoint, err := claim.Endpoint(ctx, address)
	if err != nil {
		return usb.Endpoint{}, fmt.Errorf("cmsisdap: resolve active bulk endpoint %#02x: %w", address, err)
	}
	if endpoint.Address != address || !usableBulkEndpoint(endpoint, input) {
		return usb.Endpoint{}, fmt.Errorf("cmsisdap: invalid active bulk endpoint %#02x", address)
	}
	return endpoint, nil
}

// Info returns a detached snapshot of probe and session metadata.
func (s *Session) Info() Info {
	if s == nil {
		return Info{}
	}
	return cloneInfo(s.info)
}

// Close disconnects an active target port, releases the command interface, and
// closes the USB device. A complete disconnect failure or an interface-release
// failure retains ownership for another attempt. A poisoned stream cannot
// disconnect; Close reports the abandoned port and continues USB cleanup.
// Device close runs once, and later calls return its cached result.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	if err := s.closeTargetPort(); err != nil {
		return err
	}
	if s.claim != nil {
		if err := s.claim.Close(); err != nil {
			return errors.Join(s.closePrefix, err)
		}
		s.claim = nil
	}
	if s.closeDone {
		return s.closeErr
	}
	if s.device == nil {
		return nil
	}
	s.closeErr = errors.Join(s.closePrefix, s.device.Close())
	s.closeDone = true
	s.device = nil
	return s.closeErr
}

func (s *Session) closeTargetPort() error {
	if !s.connected {
		return nil
	}
	if s.poisoned {
		s.abandonTargetPort(fmt.Errorf("cmsisdap: cannot disconnect active SWD port: %w", ErrSessionPoisoned))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := s.disconnect(ctx)
	cancel()
	if err == nil {
		return nil
	}
	if !s.poisoned {
		return err
	}
	s.abandonTargetPort(err)
	return nil
}

func (s *Session) abandonTargetPort(err error) {
	s.closePrefix = errors.Join(s.closePrefix, err)
	s.clearSWDConfiguration()
}

func (s *Session) closeAfterOpenFailure() (bool, error) {
	if s == nil {
		return false, nil
	}
	if s.connected && !s.poisoned {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := s.disconnect(ctx)
		cancel()
		if err != nil {
			if !s.poisoned {
				return true, fmt.Errorf("cmsisdap: retain SWD port after failed open: %w", err)
			}
			return false, errors.Join(err, s.Close())
		}
	}
	return false, s.Close()
}
