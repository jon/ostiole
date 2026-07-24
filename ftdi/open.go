package ftdi

import (
	"context"
	"errors"

	"github.com/jon/ostiole/usb"
)

// Open takes ownership of device and returns a ready FTDI SWD channel.
// It closes the USB device before returning any error.
func Open(
	ctx context.Context,
	device *usb.Device,
	config Config,
) (*Channel, error) {
	if device == nil {
		return nil, errors.New("ftdi: nil USB device")
	}
	return openChannel(ctx, device, config)
}

func openChannel(
	ctx context.Context,
	device usbDevice,
	config Config,
) (*Channel, error) {
	channel, err := newChannel(device, config)
	if err != nil {
		if device != nil {
			err = errors.Join(err, device.Close())
		}
		return nil, err
	}
	return prepareChannel(ctx, channel)
}

func prepareChannel(
	ctx context.Context,
	channel *Channel,
) (_ *Channel, err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, channel.Close())
		}
	}()
	if err = channel.enterMPSSE(ctx); err != nil {
		return nil, err
	}
	if err = channel.synchronize(ctx); err != nil {
		return nil, err
	}
	if err = channel.configure(ctx); err != nil {
		return nil, err
	}
	return channel, nil
}
