//go:build linux

package usb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

func (d *Device) readConfigurationDescriptor(ctx context.Context, index, total int) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("usb: nil configuration context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !d.validConfigurationRead(index, total) {
		return nil, errors.New("usb: invalid attachment configuration read")
	}
	return readAttachmentConfiguration(ctx, d.file, index, total)
}

func readAttachmentConfiguration(ctx context.Context, reader io.ReaderAt, index, total int) ([]byte, error) {
	// usbfs reads expose this open attachment's cached device descriptor followed
	// by its raw configurations, without the control ioctl's page-size limit.
	offset := int64(18)
	for current := 0; current <= index; current++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		length, err := attachmentConfigurationLength(reader, current, offset)
		if err != nil {
			return nil, err
		}
		if current == index {
			if length != total {
				return nil, fmt.Errorf("usb: attachment configuration %d length differs from device header", index)
			}
			raw := make([]byte, length)
			if _, err := reader.ReadAt(raw, offset); err != nil {
				return nil, fmt.Errorf("usb: read attachment configuration %d: %w", index, linuxUSBError(err))
			}
			return raw, nil
		}
		offset += int64(length)
	}
	return nil, errors.New("usb: attachment configuration not found")
}

func (d *Device) validConfigurationRead(index, total int) bool {
	return d != nil && d.file != nil && !d.closed && index >= 0 && index < 255 && total >= 9 && total <= 65535
}

func attachmentConfigurationLength(reader io.ReaderAt, index int, offset int64) (int, error) {
	var header [9]byte
	if _, err := reader.ReadAt(header[:], offset); err != nil {
		return 0, fmt.Errorf("usb: read attachment configuration %d header: %w", index, linuxUSBError(err))
	}
	length := int(binary.LittleEndian.Uint16(header[2:4]))
	if header[0] < 9 || int(header[0]) > length || header[1] != descriptorConfiguration || length < 9 {
		return 0, fmt.Errorf("usb: malformed attachment configuration %d header", index)
	}
	return length, nil
}
