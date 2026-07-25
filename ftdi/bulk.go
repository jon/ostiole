package ftdi

import (
	"context"
	"fmt"
	"io"
)

func (c *Channel) writeExact(ctx context.Context, data []byte) error {
	for position := 0; position < len(data); {
		count, err := c.bulkWrite(ctx, data[position:])
		if count < 0 || count > len(data)-position {
			return fmt.Errorf(
				"ftdi: invalid bulk-write count %d for %d bytes",
				count,
				len(data)-position,
			)
		}
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrNoProgress
		}
		position += count
	}
	return nil
}

func (c *Channel) readPayload(
	ctx context.Context,
	size int,
) ([]byte, error) {
	if size < 0 {
		return nil, fmt.Errorf("ftdi: negative payload size %d", size)
	}
	payload := make([]byte, 0, size)
	for len(payload) < size {
		buffer := make([]byte, readCapacity(size-len(payload), c.packetSize))
		count, err := c.bulkRead(ctx, buffer)
		if err != nil {
			return nil, err
		}
		chunk, err := decodePackets(
			buffer,
			count,
			size-len(payload),
			c.packetSize,
		)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			continue
		}
		payload = append(payload, chunk...)
	}
	return payload, nil
}

func readCapacity(payload, packetSize int) int {
	if payload <= 0 {
		return packetSize
	}
	perPacket := packetSize - 2
	packets := (payload + perPacket - 1) / perPacket
	return packets * packetSize
}

func decodePackets(
	buffer []byte,
	count, wanted, packetSize int,
) ([]byte, error) {
	if count > len(buffer) || count < 2 {
		return nil, fmt.Errorf("ftdi: invalid bulk-read count %d", count)
	}
	payload := make([]byte, 0, min(count-2, wanted))
	for position := 0; position < count; {
		packetLength := min(packetSize, count-position)
		if packetLength < 2 {
			return nil, fmt.Errorf(
				"ftdi: short status packet of %d bytes",
				packetLength,
			)
		}
		chunk := buffer[position+2 : position+packetLength]
		if len(chunk) > wanted-len(payload) {
			return nil, fmt.Errorf(
				"ftdi: received %d surplus payload bytes",
				len(chunk)-(wanted-len(payload)),
			)
		}
		payload = append(payload, chunk...)
		position += packetLength
	}
	return payload, nil
}
