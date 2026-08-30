package cmsisdap

import (
	"context"
	"errors"
	"fmt"
)

const maxSWDTransferBits = 16_384

type swdCapture struct {
	offset int
	bits   int
}

func protocolSupportsSWDSequence(version string) bool {
	var major, minor, patch int
	count, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch)
	return count == 3 && err == nil && (major > 1 || major == 1 && minor >= 2)
}

// MaxTransferBits reports the conservative logical SWDIO limit, or zero while
// SWD is not configured. One call can use several packet-sized commands.
func (s *Session) MaxTransferBits() int {
	if s == nil || !s.configured {
		return 0
	}
	return maxSWDTransferBits
}

// SWDIO executes one direction-explicit SWD bit stream. Direction and data are
// packed least-significant bit first. For each set direction bit, the probe
// drives SWDIO; for each clear bit, it samples SWDIO. Packet commands are sent
// in order and never replayed.
func (s *Session) SWDIO(ctx context.Context, direction, output []byte, bits int) ([]byte, error) {
	if bits <= 0 || bits > maxSWDTransferBits || len(direction)*8 < bits || len(output)*8 < bits {
		return nil, fmt.Errorf("cmsisdap: invalid %d-bit SWD stream", bits)
	}
	if err := s.transportReady(ctx); err != nil {
		return nil, err
	}
	if !s.configured {
		return nil, errors.New("cmsisdap: SWD is not configured")
	}

	input := make([]byte, (bits+7)/8)
	position := 0
	for packet := 1; position < bits; packet++ {
		request, captures, next := planSWDPacket(direction, output, bits, position, s.packetSize)
		if next == position {
			return nil, fmt.Errorf("cmsisdap: packet size %d cannot hold an SWD sequence", s.packetSize)
		}
		response, err := s.exchange(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("cmsisdap: DAP_SWD_Sequence packet %d: %w", packet, err)
		}
		if err := s.validateStatusResponse(response, commandSWDSequence); err != nil {
			return nil, fmt.Errorf("cmsisdap: DAP_SWD_Sequence packet %d: %w", packet, err)
		}
		if err := s.decodeSWDResponse(input, response, captures); err != nil {
			return nil, fmt.Errorf("cmsisdap: DAP_SWD_Sequence packet %d: %w", packet, err)
		}
		position = next
	}
	return input, nil
}

func planSWDPacket(direction, output []byte, bits, start, packetSize int) ([]byte, []swdCapture, int) {
	request := []byte{commandSWDSequence, 0}
	responseBytes := 2
	var captures []swdCapture
	position := start
	for position < bits && request[1] != 0xff {
		input := !swdBit(direction, position)
		run := swdRun(direction, position, bits)
		capacity := 0
		if input && len(request)+1 <= packetSize {
			capacity = (packetSize - responseBytes) * 8
		} else if !input && len(request)+1 < packetSize {
			capacity = (packetSize - len(request) - 1) * 8
		}
		count := min(run, 64, capacity)
		if count <= 0 {
			break
		}
		info := byte(count & 0x3f)
		if input {
			info |= 0x80
			captures = append(captures, swdCapture{offset: position, bits: count})
			responseBytes += (count + 7) / 8
		}
		request = append(request, info)
		if !input {
			request = append(request, packSWDBits(output, position, count)...)
		}
		request[1]++
		position += count
	}
	return request, captures, position
}

func (s *Session) decodeSWDResponse(input, response []byte, captures []swdCapture) error {
	offset := 2
	for _, capture := range captures {
		bytes := (capture.bits + 7) / 8
		if offset+bytes > len(response) {
			return s.poison(fmt.Errorf("short response: got %d bytes, need %d", len(response), offset+bytes))
		}
		for bit := range capture.bits {
			if swdBit(response[offset:offset+bytes], bit) {
				input[(capture.offset+bit)/8] |= 1 << uint((capture.offset+bit)%8)
			}
		}
		offset += bytes
	}
	return nil
}

func swdRun(direction []byte, start, bits int) int {
	value := swdBit(direction, start)
	count := 1
	for count < 64 && start+count < bits && swdBit(direction, start+count) == value {
		count++
	}
	return count
}

func packSWDBits(data []byte, start, bits int) []byte {
	packed := make([]byte, (bits+7)/8)
	for bit := range bits {
		if swdBit(data, start+bit) {
			packed[bit/8] |= 1 << uint(bit%8)
		}
	}
	return packed
}

func swdBit(data []byte, bit int) bool { return data[bit/8]&(1<<uint(bit%8)) != 0 }
