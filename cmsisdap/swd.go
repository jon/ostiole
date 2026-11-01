package cmsisdap

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const maxSWDTransferBits = 16_384

type swdCapture struct {
	offset int
	bits   int
}

func protocolSupportsSWDSequence(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	var values [3]uint64
	for i, part := range parts {
		value, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return false
		}
		values[i] = value
	}
	return values[0] > 1 || values[0] == 1 && values[1] >= 2
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
// in order and never replayed. The complete stream is validated before traffic.
// It must finish with host ownership, and each target-driven phase must fit in
// one command. These restrictions do not satisfy the general swd.Wire contract.
// After the first OUT is accepted, all packets share one independent completion
// budget. Cancellation is returned after completion; a non-nil input means the
// entire stream completed. An unfinished prefix poisons the session.
func (s *Session) SWDIO(ctx context.Context, direction, output []byte, bits int) (input []byte, err error) {
	if bits <= 0 || bits > maxSWDTransferBits || len(direction)*8 < bits || len(output)*8 < bits {
		return nil, fmt.Errorf("cmsisdap: invalid %d-bit SWD stream", bits)
	}
	if err := s.sequenceReady(ctx); err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, ctx.Err()) }()

	packets, err := planSWDStream(direction, output, bits, s.packetSize)
	if err != nil {
		return nil, err
	}
	input = make([]byte, (bits+7)/8)
	var completion commandCompletion
	defer completion.close()
	for index, packet := range packets {
		response, err := s.exchangeWithCompletion(ctx, packet.request, &completion)
		if err != nil {
			return nil, s.sequenceFailure(index, err)
		}
		if err := s.validateStatusResponse(response, commandSWDSequence); err != nil {
			return nil, s.sequenceFailure(index, err)
		}
		if err := s.decodeSWDResponse(input, response, packet.captures); err != nil {
			return nil, s.sequenceFailure(index, err)
		}
	}
	return input, nil
}

func (s *Session) sequenceFailure(index int, err error) error {
	if index > 0 {
		err = s.poison(err)
	}
	return fmt.Errorf("cmsisdap: DAP_SWD_Sequence packet %d: %w", index+1, err)
}

type swdPacket struct {
	request  []byte
	captures []swdCapture
}

func planSWDStream(direction, output []byte, bits, packetSize int) ([]swdPacket, error) {
	if !swdBit(direction, bits-1) {
		return nil, errors.New("cmsisdap: SWD sequence must end with host ownership")
	}
	var packets []swdPacket
	for position := 0; position < bits; {
		request, captures, next := planSWDPacket(direction, output, bits, position, packetSize)
		if next == position {
			return nil, fmt.Errorf("cmsisdap: packet size %d cannot preserve SWD direction boundary at bit %d", packetSize, position)
		}
		packets = append(packets, swdPacket{request: request, captures: captures})
		position = next
	}
	return packets, nil
}

func planSWDPacket(direction, output []byte, bits, start, packetSize int) ([]byte, []swdCapture, int) {
	request := []byte{commandSWDSequence, 0}
	responseBytes := 2
	var captures []swdCapture
	position := start
	for position < bits && request[1] != 0xff {
		input := !swdBit(direction, position)
		run := swdRun(direction, position, bits)
		if input && !swdCaptureFits(run, len(request), responseBytes, int(request[1]), packetSize) {
			break
		}
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

func swdCaptureFits(bits, requestBytes, responseBytes, sequences, packetSize int) bool {
	descriptors := (bits + 63) / 64
	return responseBytes+(bits+7)/8 <= packetSize &&
		requestBytes+descriptors <= packetSize && sequences+descriptors <= 255
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
	for start+count < bits && swdBit(direction, start+count) == value {
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

func (s *Session) sequenceReady(ctx context.Context) error {
	if err := s.transportReady(ctx); err != nil {
		return err
	}
	if !s.configured {
		return errors.New("cmsisdap: SWD is not configured")
	}
	if !protocolSupportsSWDSequence(s.info.ProtocolVersion) {
		return fmt.Errorf("cmsisdap: protocol version %q does not support DAP_SWD_Sequence", s.info.ProtocolVersion)
	}
	return nil
}
