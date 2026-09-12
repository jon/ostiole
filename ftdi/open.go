package ftdi

import (
	"context"
	"errors"

	"github.com/jon/ostiole/usb"
)

// Open initializes one MPSSE port and clock, leaving target pins as inputs. A non-nil channel
// owns device even on error and must be closed; retain it if Close fails.
// A nil channel leaves device with the caller. No reset pin is driven.
// Each transfer establishes its pin directions; the caller verifies wiring.
func Open(ctx context.Context, device *usb.Device, config Config) (*Channel, error) {
	if device == nil {
		return nil, errors.New("ftdi: nil USB device")
	}
	return openChannel(ctx, ownedUSBDevice{Device: device}, config)
}

func openChannel(ctx context.Context, device usbDevice, config Config) (*Channel, error) {
	if ctx == nil {
		return nil, errors.New("ftdi: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	channel, err := newChannel(device, config)
	if err != nil {
		return nil, err
	}
	_, err = prepareChannel(ctx, channel)
	return channel, err
}

func prepareChannel(ctx context.Context, channel *Channel) (_ *Channel, err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, channel.Close())
		}
	}()
	if err = channel.enterMPSSE(ctx); err != nil {
		return nil, err
	}
	if err = channel.openUSBTransfers(ctx); err != nil {
		return nil, err
	}
	if err = channel.synchronize(ctx); err != nil {
		return nil, err
	}
	if err = channel.configure(ctx); err != nil {
		return nil, err
	}
	channel.active = true
	return channel, nil
}
