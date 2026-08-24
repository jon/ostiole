package ftdi

import (
	"context"
	"fmt"
	"io"
)

const packetStatusSize = 2

func (c *Channel) writeExact(ctx context.Context, data []byte) error {
	if err := c.transportReady(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for position := 0; position < len(data); {
		count, err := c.bulkWrite(ctx, data[position:])
		if count < 0 || count > len(data)-position {
			return c.poison(fmt.Errorf("ftdi: invalid bulk-write count %d for %d bytes", count, len(data)-position))
		}
		if err != nil {
			return c.poison(err)
		}
		if count == 0 {
			return c.poison(io.ErrNoProgress)
		}
		position += count
		if err := c.transportReady(); err != nil {
			return err
		}
	}
	return nil
}

func (c *Channel) readPayload(ctx context.Context, size int) ([]byte, error) {
	if size < 0 {
		return nil, fmt.Errorf("ftdi: negative payload size %d", size)
	}
	if err := c.transportReady(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload := make([]byte, 0, size)
	for len(payload) < size {
		var chunk []byte
		select {
		case chunk = <-c.receive:
		case err := <-c.receiveErr:
			return nil, c.poison(err)
		case <-ctx.Done():
			return nil, c.poison(ctx.Err())
		}
		if len(chunk) > size-len(payload) {
			return nil, c.poison(fmt.Errorf("ftdi: received %d surplus payload bytes", len(chunk)-(size-len(payload))))
		}
		payload = append(payload, chunk...)
	}
	return payload, nil
}

func (c *Channel) exchangePayload(ctx context.Context, output []byte, size int) ([]byte, error) {
	if size < 0 {
		return nil, fmt.Errorf("ftdi: negative payload size %d", size)
	}
	if err := c.beginResponse(); err != nil {
		return nil, c.poison(err)
	}
	defer c.endResponse()
	if err := c.writeExact(ctx, output); err != nil {
		return nil, err
	}
	return c.readPayload(ctx, size)
}

func receiveDepth(payload, packetSize int) int {
	if payload <= 0 {
		return 1
	}
	perPacket := packetSize - packetStatusSize
	return (payload + perPacket - 1) / perPacket
}

func decodeCompletion(buffer []byte, packetSize int) ([]byte, error) {
	if len(buffer) < packetStatusSize {
		return nil, fmt.Errorf("ftdi: invalid bulk-read count %d", len(buffer))
	}
	payload := make([]byte, 0, len(buffer)-packetStatusSize)
	for position := 0; position < len(buffer); {
		packetLength := min(packetSize, len(buffer)-position)
		if packetLength < packetStatusSize {
			return nil, fmt.Errorf("ftdi: short status packet of %d bytes", packetLength)
		}
		payload = append(payload, buffer[position+packetStatusSize:position+packetLength]...)
		position += packetLength
	}
	return payload, nil
}
