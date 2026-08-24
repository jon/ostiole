package jlink

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/jon/ostiole/usb"
)

const (
	commandVersion            = 0x01
	commandInterface          = 0xc7
	commandWorkspace          = 0xd4
	commandCapabilitiesShort  = 0xe8
	commandCapabilitiesLong   = 0xed
	commandHardwareVersion    = 0xf0
	capabilityHardwareVersion = 1
	capabilityWorkspace       = 11
	capabilityInterface       = 17
	capabilityLong            = 31
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

const (
	configurationInspectionAttempts = 101
	configurationInspectionInterval = 10 * time.Millisecond
	configurationInspectionTimeout  = time.Second
)

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

// Session owns one J-Link application interface and its USB device. Calls on a
// session must be serialized.
type Session struct {
	device       usbDevice
	claim        usbClaim
	application  applicationInterface
	input        []byte
	info         Info
	poisoned     bool
	configured   bool
	clockHz      uint32
	transferBits int
	delayInput   bool
	inputCarry   bool
	closeDone    bool
	closeErr     error
}

// Open claims the J-Link application interface, reads probe metadata, applies
// its options, and takes ownership of the device on success. With no options it
// does not select or configure a target interface. After an error, the caller
// must still call device.Close; Open has already attempted cleanup.
func Open(ctx context.Context, device *usb.Device, options ...Option) (*Session, error) {
	if device == nil {
		return nil, errors.New("jlink: nil USB device")
	}
	return openSession(ctx, ownedUSBDevice{Device: device}, options...)
}

func openSession(ctx context.Context, device usbDevice, options ...Option) (_ *Session, err error) {
	if device == nil {
		return nil, errors.New("jlink: nil USB device")
	}
	config, err := prepareOpen(ctx, options)
	if err != nil {
		return nil, errors.Join(err, device.Close())
	}
	identity := device.Identity()
	if !supportedDevice(identity) {
		return nil, errors.Join(fmt.Errorf("jlink: unsupported USB identity %04x:%04x", identity.VID, identity.PID), device.Close())
	}
	application, err := inspectApplication(ctx, device)
	if err != nil {
		return nil, errors.Join(err, device.Close())
	}
	session := &Session{device: device, application: application, info: Info{USB: identity}}
	defer func() {
		if err != nil {
			err = errors.Join(err, session.Close())
		}
	}()
	claim, err := device.claimInterface(application.number)
	if err != nil {
		return nil, fmt.Errorf("jlink: claim USB interface %d: %w", application.number, err)
	}
	session.claim = claim
	if err := claim.SetAltSetting(application.alternate); err != nil {
		return nil, fmt.Errorf("jlink: select USB interface %d alternate %d: %w", application.number, application.alternate, err)
	}
	bulkIn, err := selectedEndpoint(ctx, claim, application.bulkIn.Address, "IN")
	if err != nil {
		return nil, err
	}
	bulkOut, err := selectedEndpoint(ctx, claim, application.bulkOut.Address, "OUT")
	if err != nil {
		return nil, err
	}
	session.application.bulkIn, session.application.bulkOut = bulkIn, bulkOut
	if err := session.readInfo(ctx); err != nil {
		return nil, err
	}
	if err := configureOpen(ctx, session, config); err != nil {
		return nil, err
	}
	return session, nil
}

func configureOpen(ctx context.Context, session *Session, config openConfig) error {
	if !config.configureSWD {
		return nil
	}
	return session.ConfigureSWD(ctx, config.maxClockHz)
}

func selectedEndpoint(ctx context.Context, claim usbClaim, address uint8, direction string) (usb.Endpoint, error) {
	endpoint, err := claim.Endpoint(ctx, address)
	if err != nil {
		return usb.Endpoint{}, fmt.Errorf("jlink: resolve active bulk %s endpoint %#02x: %w", direction, address, err)
	}
	if endpoint.Address != address || !usableBulkEndpoint(endpoint) {
		return usb.Endpoint{}, fmt.Errorf("jlink: invalid active bulk %s endpoint %#02x", direction, address)
	}
	return endpoint, nil
}

func prepareOpen(ctx context.Context, options []Option) (openConfig, error) {
	if ctx == nil {
		return openConfig{}, errors.New("jlink: nil open context")
	}
	if err := ctx.Err(); err != nil {
		return openConfig{}, err
	}
	return applyOptions(options)
}

func applyOptions(options []Option) (openConfig, error) {
	var config openConfig
	for index, option := range options {
		if option.apply == nil {
			continue
		}
		if err := option.apply(&config); err != nil {
			return openConfig{}, fmt.Errorf("jlink: option %d: %w", index, err)
		}
	}
	return config, nil
}

func inspectApplication(ctx context.Context, device usbDevice) (applicationInterface, error) {
	inspectionCtx, cancel := context.WithTimeout(ctx, configurationInspectionTimeout)
	defer cancel()
	return inspectApplicationWithWait(inspectionCtx, device, waitForConfigurationInspection)
}

func inspectApplicationWithWait(ctx context.Context, device usbDevice, wait func(context.Context) error) (applicationInterface, error) {
	var unavailable error
	for attempt := range configurationInspectionAttempts {
		configuration, err := device.ActiveConfiguration(ctx)
		if err == nil {
			return findApplicationInterface(configuration)
		}
		if !errors.Is(err, usb.ErrNotConfigured) {
			if unavailable != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				err = errors.Join(unavailable, err)
			}
			return applicationInterface{}, fmt.Errorf("jlink: inspect active USB configuration: %w", err)
		}
		unavailable = err
		if attempt == configurationInspectionAttempts-1 {
			return applicationInterface{}, fmt.Errorf("jlink: inspect active USB configuration: %w", err)
		}
		if waitErr := wait(ctx); waitErr != nil {
			return applicationInterface{}, fmt.Errorf("jlink: inspect active USB configuration: %w", errors.Join(err, waitErr))
		}
	}
	panic("unreachable")
}

func waitForConfigurationInspection(ctx context.Context) error {
	timer := time.NewTimer(configurationInspectionInterval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) readInfo(ctx context.Context) error {
	if err := s.readVersion(ctx); err != nil {
		return err
	}
	if err := s.readCapabilities(ctx); err != nil {
		return err
	}
	if s.info.Capabilities.Has(capabilityHardwareVersion) {
		response, err := s.exchange(ctx, []byte{commandHardwareVersion}, 4)
		if err != nil {
			return fmt.Errorf("jlink: read hardware version: %w", err)
		}
		s.info.Hardware = decodeHardwareVersion(binary.LittleEndian.Uint32(response))
		s.info.HardwareKnown = true
	}
	if s.info.Capabilities.Has(capabilityWorkspace) {
		response, err := s.exchange(ctx, []byte{commandWorkspace}, 4)
		if err != nil {
			return fmt.Errorf("jlink: read workspace: %w", err)
		}
		s.info.Workspace = binary.LittleEndian.Uint32(response)
		s.info.WorkspaceKnown = true
	}
	if s.info.Capabilities.Has(capabilityInterface) {
		return s.readInterfaces(ctx)
	}
	return nil
}

func (s *Session) readVersion(ctx context.Context) error {
	if err := s.writeExact(ctx, []byte{commandVersion}); err != nil {
		return fmt.Errorf("jlink: read firmware length: %w", err)
	}
	length, err := s.readResponsePart(ctx, 2)
	if err != nil {
		return fmt.Errorf("jlink: read firmware length: %w", err)
	}
	record, err := s.readResponse(ctx, int(binary.LittleEndian.Uint16(length)))
	if err != nil {
		return fmt.Errorf("jlink: read firmware record: %w", err)
	}
	s.info.FirmwareRecord = record
	s.info.Firmware = firmwareString(record)
	return nil
}

func (s *Session) readCapabilities(ctx context.Context) error {
	short, err := s.exchange(ctx, []byte{commandCapabilitiesShort}, 4)
	if err != nil {
		return fmt.Errorf("jlink: read capabilities: %w", err)
	}
	s.info.Capabilities = Capabilities{bytes: short}
	if s.info.Capabilities.Has(capabilityLong) {
		long, err := s.exchange(ctx, []byte{commandCapabilitiesLong}, 32)
		if err != nil {
			return fmt.Errorf("jlink: read extended capabilities: %w", err)
		}
		if string(long[:4]) != string(short) {
			return errors.New("jlink: extended capabilities do not preserve the short bitset")
		}
		s.info.Capabilities = Capabilities{bytes: long}
	}
	return nil
}

func (s *Session) readInterfaces(ctx context.Context) error {
	response, err := s.exchange(ctx, []byte{commandInterface, 0xff}, 4)
	if err != nil {
		return fmt.Errorf("jlink: read available target interfaces: %w", err)
	}
	s.info.AvailableInterfaces = binary.LittleEndian.Uint32(response)
	response, err = s.exchange(ctx, []byte{commandInterface, 0xfe}, 4)
	if err != nil {
		return fmt.Errorf("jlink: read selected target interface: %w", err)
	}
	selected := binary.LittleEndian.Uint32(response)
	if selected >= 32 {
		return fmt.Errorf("jlink: invalid selected target interface %d", selected)
	}
	s.info.SelectedInterface = uint8(selected)
	s.info.SelectedInterfaceKnown = true
	return nil
}

// Info returns a detached snapshot of probe and session metadata.
func (s *Session) Info() Info {
	if s == nil {
		return Info{}
	}
	return cloneInfo(s.info)
}

// Close releases the application interface and closes the USB device. A
// failed interface release retains the claim so Close can retry it. Device
// close runs once; later calls return its cached result.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	if s.claim != nil {
		if err := s.claim.Close(); err != nil {
			return err
		}
		s.claim = nil
	}
	if s.closeDone {
		return s.closeErr
	}
	if s.device == nil {
		return nil
	}
	s.closeErr = s.device.Close()
	s.closeDone = true
	s.device = nil
	return s.closeErr
}
